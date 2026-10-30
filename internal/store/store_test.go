package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/strata/internal/model"
)

var ctx = context.Background()

// open returns a store in a temp file.
func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "strata.duckdb")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// image returns a minimal image record.
func image(digest, repo string) *model.Image {
	return &model.Image{
		Digest: digest, Repo: repo, Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Labels: map[string]string{"team": "x"}, ChainIDs: []string{"sha256:c1"},
		Layers:  []model.Layer{{DiffID: "sha256:c1", BlobDigest: "sha256:b1", Size: 10}},
		History: []model.HistoryEntry{{CreatedBy: "RUN x"}},
		Config:  model.Config{EnvKeys: []string{"A"}, EnvValues: map[string]string{"A": "1"}},
	}
}

func TestOpenAndMeta(t *testing.T) {
	t.Parallel()
	s, path := open(t)
	if s.Path() != path {
		t.Fatalf("Path = %s", s.Path())
	}
	v, err := s.Meta(ctx, "format_version")
	if err != nil || v != "1" {
		t.Fatalf("format_version = %q, %v", v, err)
	}
	if v, err := s.Meta(ctx, "engine_version"); err != nil || !strings.HasPrefix(v, "v") {
		t.Fatalf("engine_version = %q, %v", v, err)
	}
	if v, err := s.Meta(ctx, "missing"); err != nil || v != "" {
		t.Fatalf("missing meta = %q, %v", v, err)
	}
	// A file written by a newer strata is refused.
	if err := s.SetMeta(ctx, "format_version", "99"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("newer format error = %v", err)
	}
	s2, err := Open(ctx, filepath.Join(t.TempDir(), "x.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.SetMeta(ctx, "format_version", "abc"); err != nil {
		t.Fatal(err)
	}
	p := s2.Path()
	_ = s2.Close()
	if _, err := Open(ctx, p); err == nil {
		t.Fatal("non-numeric format version accepted")
	}
}

func TestOpenErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, filepath.Join(blocker, "sub", "x.duckdb")); err == nil {
		t.Fatal("Open under a file succeeded")
	}
	if _, err := Open(ctx, dir); err == nil {
		t.Fatal("Open of a directory succeeded")
	}
}

func TestWrite(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	now := time.Now().UTC()
	b := &Batch{
		Images:     []*model.Image{image("sha256:a", "reg/x"), image("sha256:b", "reg/x")},
		Index:      []model.IndexEntry{{IndexDigest: "sha256:i", Digest: "sha256:a", Platform: model.Platform{OS: "linux", Arch: "amd64"}}},
		Tags:       []model.Tag{{Repo: "reg/x", Tag: "v1", Digest: "sha256:i", UpdatedAt: now}, {Repo: "reg/x", Tag: "v2", Digest: "sha256:b", UpdatedAt: now}},
		Events:     []model.TagEvent{{Repo: "reg/x", Tag: "v1", SeenAt: now, Digest: "sha256:i", Source: "crawl"}},
		Referrers:  []model.Referrer{{Subject: "sha256:a", Digest: "sha256:s", Kind: model.KindSBOM}},
		DeleteTags: []model.Tag{{Repo: "reg/x", Tag: "gone"}},
	}
	if b.Len() != 8 {
		t.Fatalf("Len = %d", b.Len())
	}
	if err := s.Write(ctx, b); err != nil {
		t.Fatal(err)
	}
	// Writing the same images again leaves them alone.
	if err := s.Write(ctx, &Batch{Images: []*model.Image{image("sha256:a", "reg/other")}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, &Batch{}); err != nil {
		t.Fatal(err)
	}
	var repo, team string
	if err := s.DB().QueryRow(`SELECT i.repo, l.labels['team']::VARCHAR FROM images i JOIN labels l USING (digest) WHERE digest = 'sha256:a'`).Scan(&repo, &team); err != nil {
		t.Fatal(err)
	}
	if repo != "reg/x" || team != "x" {
		t.Fatalf("image a = %s %s", repo, team)
	}
	known, err := s.KnownDigests(ctx, "reg/x")
	if err != nil || !known["sha256:a"] || !known["sha256:i"] || len(known) != 3 {
		t.Fatalf("KnownDigests = %v, %v", known, err)
	}
	for d, want := range map[string]bool{"sha256:a": true, "sha256:i": true, "sha256:zzz": false} {
		if got, err := s.KnownImage(ctx, d); err != nil || got != want {
			t.Errorf("KnownImage(%s) = %v, %v", d, got, err)
		}
	}
	tags, err := s.Tags(ctx, "reg/x")
	if err != nil || tags["v1"] != "sha256:i" || len(tags) != 2 {
		t.Fatalf("Tags = %v, %v", tags, err)
	}
	if err := s.Write(ctx, &Batch{DeleteTags: []model.Tag{{Repo: "reg/x", Tag: "v1"}}}); err != nil {
		t.Fatal(err)
	}
	if tags, _ := s.Tags(ctx, "reg/x"); len(tags) != 1 {
		t.Fatalf("tag not deleted: %v", tags)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Write(canceled, b); err == nil {
		t.Fatal("Write on a canceled context succeeded")
	}
}

func TestCrawlRuns(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	run, done, err := s.StartCrawl(ctx, "reg")
	if err != nil || len(done) != 0 {
		t.Fatalf("StartCrawl = %d %v %v", run, done, err)
	}
	if err := s.CheckpointRepo(ctx, run, "reg/a"); err != nil {
		t.Fatal(err)
	}
	again, done, err := s.StartCrawl(ctx, "reg")
	if err != nil || again != run || !done["reg/a"] {
		t.Fatalf("resume = %d %v %v", again, done, err)
	}
	if err := s.FinishCrawl(ctx, run); err != nil {
		t.Fatal(err)
	}
	next, done, err := s.StartCrawl(ctx, "reg")
	if err != nil || next == run || len(done) != 0 {
		t.Fatalf("next run = %d %v %v", next, done, err)
	}
}

func TestHistoryParquet(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	now := time.Now().UTC()
	events := []model.TagEvent{
		{Repo: "reg/x", Tag: "v1", SeenAt: now, Digest: "sha256:a", Source: "crawl"},
		{Repo: "reg/x", Tag: "v1", SeenAt: now.Add(time.Hour), Source: "crawl"},
	}
	if err := s.Write(ctx, &Batch{Events: events}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "it's.parquet")
	n, err := s.ExportHistory(ctx, file)
	if err != nil || n != 2 {
		t.Fatalf("export = %d, %v", n, err)
	}
	s2, _ := open(t)
	if n, err := s2.ImportHistory(ctx, file); err != nil || n != 2 {
		t.Fatalf("import = %d, %v", n, err)
	}
	if n, err := s2.ImportHistory(ctx, file); err != nil || n != 0 {
		t.Fatalf("re-import = %d, %v", n, err)
	}
	if _, err := s2.ImportHistory(ctx, filepath.Join(t.TempDir(), "missing.parquet")); err == nil {
		t.Fatal("import of a missing file succeeded")
	}
	if _, err := s2.ExportHistory(ctx, filepath.Join(t.TempDir(), "no", "dir", "x.parquet")); err == nil {
		t.Fatal("export to a missing directory succeeded")
	}
}

func TestInMemory(t *testing.T) {
	t.Parallel()
	s, err := Open(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.Path() != "" {
		t.Fatal("in-memory store has a path")
	}
	if nullable("") != nil || nullable("x") != "x" || len(nonNil(nil)) != 0 {
		t.Fatal("helpers")
	}
}

func BenchmarkWrite(b *testing.B) {
	s, err := Open(ctx, "")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		batch := &Batch{}
		for j := 0; j < 100; j++ {
			i++
			img := image("sha256:"+strings.Repeat("0", 10)+string(rune('a'+i%26))+time.Now().Format("150405.000000000"), "reg/x")
			batch.Images = append(batch.Images, img)
		}
		if err := s.Write(ctx, batch); err != nil {
			b.Fatal(err)
		}
	}
}
