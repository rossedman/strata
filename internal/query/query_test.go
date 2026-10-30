package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/lineage"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/stale"
	"github.com/rossedman/strata/internal/store"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{`label.team = "payments" and age > 180d`, `(label.team = "payments" and age > 180d)`},
		{`ancestor("reg.io/base/go:1.21") and not has:sbom`, `(ancestor("reg.io/base/go:1.21") and not has:sbom)`},
		{`stale in ["stale", "abandoned"] and label.env = "prod"`, `(stale in ["stale", "abandoned"] and label.env = "prod")`},
		{`root ~ "reg.io/base/debian:*" and label.deprecated exists`, `(root ~ "reg.io/base/debian:*" and label.deprecated exists)`},
		{`a = 1 or b = 2 and c = 3`, `(a = 1 or (b = 2 and c = 3))`},
		{`(a = 1 or b = 2) and c = 3`, `((a = 1 or b = 2) and c = 3)`},
		{`NOT not x`, `not not x`},
		{`size >= 1.5GiB # a comment`, `size >= 1.5GiB`},
		{`label.x = 'it\'s'`, `label.x = "it's"`},
		{`tags = 0 and descendants = 0`, `(tags = 0 and descendants = 0)`},
		{`f(1, "a")`, `f(1, "a")`},
		{`f()`, `f()`},
		{`created < 2026-01-01`, `created < 2026-01-01`},
		{`x != -3`, `x != -3`},
	}
	for _, tt := range tests {
		n, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if got := n.String(); got != tt.want {
			t.Errorf("Parse(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
	if n, err := Parse("   "); n != nil || err != nil {
		t.Fatalf("empty filter = %v, %v", n, err)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, msg string }{
		{`a =`, "expected a value"},
		{`a = "x`, "unterminated string"},
		{`(a = 1`, `expected ")"`},
		{`a = 1 b = 2`, `unexpected "b"`},
		{`and = 1`, "keyword"},
		{`a in [1 2]`, `expected "," or "]"`},
		{`a in []`, "empty list"},
		{`a in 1`, `expected "["`},
		{`f(1 2)`, `expected "," or ")"`},
		{`a ! b`, `"!" must be followed`},
		{`a = $`, "unexpected character"},
		{`= 1`, "expected a field"},
		{`f(`, "expected a value"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.in)
		var se *SyntaxError
		if !errors.As(err, &se) || !strings.Contains(err.Error(), tt.msg) {
			t.Errorf("Parse(%q) error = %v, want %q", tt.in, err, tt.msg)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, msg string }{
		{`nope = 1`, "unknown field"},
		{`tag < "x"`, "does not apply"},
		{`has:sbom = maybe`, "expected true or false"},
		{`has:sbom > true`, "does not apply"},
		{`layers = many`, "whole number"},
		{`size > 3 parsecs`, "unexpected"},
		{`size > 3XB`, "unknown size unit"},
		{`age > 3q`, "unknown duration unit"},
		{`age > x`, "not a number"},
		{`created > yesterday`, "expected a date"},
		{`stale = rotten`, "unknown verdict"},
		{`layers ~ "1*"`, "does not apply"},
		{`has:sbom in [true]`, `"in" does not apply`},
		{`ancestor()`, "one ref"},
		{`frobnicate("x")`, "unknown function"},
		{`not nope`, "unknown field"},
		{`nope exists and repo = "x"`, "unknown field"},
		{`repo = "x" or nope`, "unknown field"},
		{`nope in [1]`, "unknown field"},
		{`layers in [x]`, "whole number"},
		{`size = 1e400`, "not a number"},
		{`size = 1e18TiB`, "out of range"},
		{`a = `, "expected a value"},
	}
	for _, tt := range tests {
		_, err := Compile(tt.in)
		if err == nil || !strings.Contains(err.Error(), tt.msg) {
			t.Errorf("Compile(%q) error = %v, want %q", tt.in, err, tt.msg)
		}
	}
}

func TestUnits(t *testing.T) {
	t.Parallel()
	days := map[string]float64{"180d": 180, "12h": 0.5, "2w": 14, "1y": 365, "3mo": 90, "7": 7}
	for in, want := range days {
		if got, err := ParseDays(in); err != nil || got != want {
			t.Errorf("ParseDays(%s) = %v, %v", in, got, err)
		}
	}
	sizes := map[string]int64{"512": 512, "10KB": 10000, "1.5GiB": 1610612736, "2mb": 2000000, "1B": 1}
	for in, want := range sizes {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("ParseBytes(%s) = %v, %v", in, got, err)
		}
	}
}

func TestLookupAndExpr(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"parent", "root", "label.a.b", "repo"} {
		if _, err := Expr(name); err != nil {
			t.Errorf("Expr(%s): %v", name, err)
		}
	}
	if _, err := Expr("nope"); err == nil {
		t.Fatal("Expr accepted an unknown field")
	}
	if !IsList("tag") || IsList("repo") || IsList("nope") {
		t.Fatal("IsList is wrong")
	}
	if len(Fields()) != len(staticFields)+2 {
		t.Fatal("Fields is missing entries")
	}
	if _, err := Lookup("label."); err == nil {
		t.Fatal("empty label key accepted")
	}
}

// registryDB builds a small indexed registry with lineage and staleness.
func registryDB(t *testing.T) (*store.Store, map[string]*model.Image) {
	t.Helper()
	imgs := map[string]*model.Image{
		"debian":  fixture.Image("debian", "docker.io/library/debian", "d1"),
		"go":      fixture.Image("go", "reg.io/base/go", "d1", "g1"),
		"api":     fixture.Created(fixture.Image("api", "reg.io/payments/api", "d1", "g1", "a1"), -4000),
		"worker":  fixture.Image("worker", "reg.io/payments/worker", "d1", "g1", "w1"),
		"web":     fixture.Image("web", "reg.io/web/site", "d1", "s1"),
		"orphan":  fixture.Image("orphan", "reg.io/web/old", "o1"),
		"oldbase": fixture.Image("oldbase", "reg.io/base/go", "d1", "g0"),
	}
	fixture.Label(imgs["api"], "team", "payments")
	fixture.Label(imgs["api"], "env", "prod")
	fixture.Label(imgs["worker"], "team", "payments")
	fixture.Label(imgs["web"], "team", "web")
	fixture.Label(imgs["web"], "deprecated", "true")
	fixture.Label(imgs["web"], "org.opencontainers.image.source", "https://github.com/x/web")
	imgs["api"].Config.User = "root"
	imgs["api"].Config.EnvKeys = []string{"PATH", "AWS_SECRET_ACCESS_KEY"}
	imgs["api"].Config.Ports = []string{"22/tcp"}
	imgs["worker"].Config.Healthcheck = true
	b := &store.Batch{
		Tags: []model.Tag{
			fixture.Tag(imgs["debian"], "bookworm-slim", 0),
			fixture.Tag(imgs["go"], "1.22", 0),
			fixture.Tag(imgs["api"], "1.8.2", 0),
			fixture.Tag(imgs["api"], "latest", 0),
			fixture.Tag(imgs["worker"], "v1", 0),
			fixture.Tag(imgs["web"], "v3", 0),
		},
		Referrers: []model.Referrer{{Subject: imgs["worker"].Digest, Digest: fixture.Digest("s"), Kind: model.KindSBOM}},
	}
	for _, img := range imgs {
		b.Images = append(b.Images, img)
	}
	s := fixture.Open(t, b)
	ctx := context.Background()
	if _, err := lineage.Recompute(ctx, s.DB(), lineage.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Recompute(ctx, s.DB(), stale.DefaultPolicy(), fixture.Epoch); err != nil {
		t.Fatal(err)
	}
	return s, imgs
}

func TestFiltersAgainstIndex(t *testing.T) {
	t.Parallel()
	s, _ := registryDB(t)
	tests := []struct {
		filter string
		repos  string
	}{
		{`label.team = "payments"`, "reg.io/payments/api reg.io/payments/worker"},
		{`label.team = "payments" and age > 3000d`, "reg.io/payments/api"},
		{`label.team != "payments"`, "docker.io/library/debian reg.io/base/go reg.io/base/go reg.io/web/old reg.io/web/site"},
		{`ancestor("reg.io/base/go:1.22")`, "reg.io/payments/api reg.io/payments/worker"},
		{`ancestor("docker.io/library/debian:bookworm-slim") and not has:sbom`, "reg.io/base/go reg.io/base/go reg.io/payments/api reg.io/web/site"},
		{`has:sbom`, "reg.io/payments/worker"},
		{`root ~ "docker.io/library/debian:*" and label.deprecated exists`, "reg.io/web/site"},
		{`label.deprecated`, "reg.io/web/site"},
		{`parent = "reg.io/base/go:1.22"`, "reg.io/payments/api reg.io/payments/worker"},
		{`parent exists and descendants = 0 and repo ~ "reg.io/web/*"`, "reg.io/web/site"},
		{`tags = 0 and descendants = 0`, "reg.io/base/go reg.io/web/old"},
		{`tag = "latest"`, "reg.io/payments/api"},
		{`tag ~ "1.*"`, "reg.io/base/go reg.io/payments/api"},
		{`tag in ["v1", "v3"]`, "reg.io/payments/worker reg.io/web/site"},
		{`ref = "reg.io/payments/api:1.8.2"`, "reg.io/payments/api"},
		{`config.user in ["", "0", "root"] and label.env = "prod"`, "reg.io/payments/api"},
		{`config.env_keys ~ "*SECRET*"`, "reg.io/payments/api"},
		{`config.ports = "22/tcp"`, "reg.io/payments/api"},
		{`not config.healthcheck and label.team = "payments"`, "reg.io/payments/api"},
		{`config.healthcheck = true`, "reg.io/payments/worker"},
		{`missing:team and repo ~ "reg.io/*"`, "reg.io/base/go reg.io/base/go reg.io/web/old"},
		{`label.org.opencontainers.image.source exists`, "reg.io/web/site"},
		{`stale = "stale"`, "reg.io/web/site"},
		{`stale in ["abandoned"]`, "reg.io/base/go reg.io/web/old"},
		{`stale.label_policy`, "reg.io/web/site"},
		{`stale and repo = "reg.io/web/site"`, "reg.io/web/site"},
		{`size > 5KB`, "reg.io/payments/api reg.io/payments/worker"},
		{`layers >= 3 and platform = "linux/amd64"`, "reg.io/payments/api reg.io/payments/worker"},
		{`created < 2025-12-31`, "reg.io/payments/api"},
		{`descendants > 1 and external = false`, "docker.io/library/debian reg.io/base/go"},
		{`children = 2 and generation = 1`, "reg.io/base/go"},
		{`conflict or parent_source = "label"`, ""},
		{`stale.base_drift or stale.untagged = false and repo = "reg.io/web/site"`, "reg.io/web/site"},
		{`tag != "latest" and repo ~ "*/payments/*"`, "reg.io/payments/worker"},
		{`parent != "reg.io/base/go:1.22" and repo ~ "*/payments/*"`, ""},
		{`config.entrypoint exists or config.cmd exists`, ""},
		{`root exists and label.team exists`, "reg.io/payments/api reg.io/payments/worker reg.io/web/site"},
		{`stale ~ "aband*"`, "reg.io/base/go reg.io/web/old"},
		{`stale.untagged != true and label.team = "web"`, "reg.io/web/site"},
		{`label.team ~ "pay*" and layers in [3]`, "reg.io/payments/api reg.io/payments/worker"},
		{``, "docker.io/library/debian reg.io/base/go reg.io/base/go reg.io/payments/api reg.io/payments/worker reg.io/web/old reg.io/web/site"},
	}
	for _, tt := range tests {
		c, err := Compile(tt.filter)
		if err != nil {
			t.Errorf("Compile(%q): %v", tt.filter, err)
			continue
		}
		var got string
		q := "SELECT coalesce(string_agg(repo, ' ' ORDER BY repo), '') FROM image_view WHERE " + c.Where
		if err := s.DB().QueryRow(q, c.Args...).Scan(&got); err != nil {
			t.Errorf("%q: %v\n%s", tt.filter, err, c.Where)
			continue
		}
		if got != tt.repos {
			t.Errorf("%q matched [%s], want [%s]", tt.filter, got, tt.repos)
		}
	}
}

func BenchmarkCompile(b *testing.B) {
	f := `label.team = "payments" and age > 180d and (stale in ["stale", "abandoned"] or not has:sbom) and root ~ "reg.io/base/*"`
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Compile(f); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzCompile(f *testing.F) {
	for _, s := range []string{
		`label.team = "payments" and age > 180d`,
		`ancestor("reg.io/base/go:1.21") and not has:sbom`,
		`stale in ["stale", "abandoned"] and label.env = "prod"`,
		`root ~ "reg.io/base/debian:*" and label.deprecated exists`,
		`tags = 0 and descendants = 0`, `size > 1.5GiB`, `(a`, `"`, `x in [`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		n, err := Parse(src)
		if err != nil {
			var se *SyntaxError
			if !errors.As(err, &se) {
				t.Fatalf("Parse error is not a SyntaxError: %v", err)
			}
			return
		}
		// Printing and re-parsing gives the same tree.
		if n != nil {
			again, err := Parse(n.String())
			if err != nil || again.String() != n.String() {
				t.Fatalf("round trip %q -> %q: %v", src, n.String(), err)
			}
		}
		c, err := CompileNode(n)
		if err != nil {
			return
		}
		// Every value is passed as a parameter, never spliced into SQL.
		if got := strings.Count(c.Where, "?"); got != len(c.Args) {
			t.Fatalf("%d placeholders for %d args in %s", got, len(c.Args), c.Where)
		}
	})
}
