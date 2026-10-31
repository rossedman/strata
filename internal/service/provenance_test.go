package service

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/testreg"
)

// TestProvenanceWalksToBasesElsewhere has an application registry whose
// image was built FROM a base in another registry, on a base version
// whose tag has since moved. Only the application registry is crawled:
// provenance names the base by digest, the crawl fetches it, and lineage
// walks the image up to it.
func TestProvenanceWalksToBasesElsewhere(t *testing.T) {
	t.Parallel()
	bases := testreg.Start(t)
	apps := testreg.Start(t)
	when := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)

	// node:20 as the app was built on it, then rebuilt: the tag moved.
	nodeOld := bases.Layered("debian", "node-old", when, nil, "RUN install node 20.1")
	bases.Push("base/node:20", nodeOld)
	bases.Push("base/node:20", bases.Layered("debian", "node-new", when, nil, "RUN install node 20.2"))
	// A builder stage's image, and a base since deleted.
	builder := bases.Layered("debian", "builder", when, nil, "RUN install compilers")
	bases.Push("base/builder:1", builder)
	gone := bases.Layered("debian", "gone", when, nil, "RUN install gone")
	bases.Push("base/gone:1", gone)
	goneDigest := bases.Digest("gone")
	bases.Delete("base/gone@" + goneDigest)

	// svc is built on the old node:20 from the base registry.
	app := bases.Layered("node-old", "svc-on-node", when, nil, "COPY app /")
	apps.PushIndex("team/svc:v1", apps.WithProvenance(app, map[string]string{
		"pkg:docker/" + bases.Host + "/base/node@20?platform=linux%2Famd64":   bases.Digest("node-old"),
		"pkg:docker/" + bases.Host + "/base/builder@1?platform=linux%2Famd64": bases.Digest("builder"),
		"pkg:docker/" + bases.Host + "/base/gone@1":                           goneDigest,
		"pkg:docker/docker/dockerfile@1":                                      bases.Digest("debian"),
	}))
	tool := apps.Layered("debian", "tool", when, nil, "RUN make tool")
	apps.Push("team/tool:v1", tool)

	svc := New(fixture.Open(t, nil))
	first, err := svc.Index(ctx, crawl.Options{
		Registry: apps.Host, Repos: []string{"team/svc", "team/tool"}, Insecure: true, Keychain: authn.NewMultiKeychain(),
		Provenance: true, FetchBases: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Crawl.BasesFetched != 2 || first.Crawl.BasesMissing != 1 {
		t.Fatalf("first crawl = %+v", first.Crawl)
	}
	digest := bases.Digest("svc-on-node")
	var parent, source, root string
	if err := svc.db().QueryRow(`SELECT l.parent, l.parent_source, coalesce(r.repo, '') FROM lineage l LEFT JOIN images r ON r.digest = l.parent WHERE l.digest = ?`, digest).
		Scan(&parent, &source, &root); err != nil {
		t.Fatal(err)
	}
	if parent != bases.Digest("node-old") || root != bases.Host+"/base/node" {
		t.Fatalf("svc's parent = %s (%s, %s), want the old node:20", parent, source, root)
	}
	// The provenance edge is kept for node, not for the builder stage.
	var evidence string
	if err := svc.db().QueryRow(`SELECT string_agg(evidence, ' | ') FROM edges WHERE child = ? AND source = 'attestation'`, digest).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(evidence, "base/node:20@") || strings.Contains(evidence, "builder") {
		t.Fatalf("attestation evidence = %q", evidence)
	}
	// The fetched bases are marked external: the base registry is not
	// one the index crawls.
	var external bool
	if err := svc.db().QueryRow(`SELECT external FROM images WHERE digest = ?`, parent).Scan(&external); err != nil || !external {
		t.Fatalf("node external = %v, %v", external, err)
	}
	// The deleted base is recorded with why, and the frontend image was
	// never a candidate.
	var why string
	if err := svc.db().QueryRow(`SELECT error FROM base_fetches WHERE base_digest = ?`, goneDigest).Scan(&why); err != nil || why == "" {
		t.Fatalf("deleted base fetch = %q, %v", why, err)
	}
	var n int
	if err := svc.db().QueryRow(`SELECT count(*) FROM base_refs WHERE ref LIKE '%dockerfile%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("frontend refs = %d, %v", n, err)
	}

	// A second crawl neither refetches nor retries within a week.
	st, err := svc.Index(ctx, crawl.Options{
		Registry: apps.Host, Repos: []string{"team/svc"}, Insecure: true, Keychain: authn.NewMultiKeychain(),
		Provenance: true, FetchBases: true,
	})
	if err != nil || st.Crawl.BasesFetched != 0 || st.Crawl.BasesMissing != 0 {
		t.Fatalf("second crawl = %+v, %v", st.Crawl, err)
	}
}

// TestProvenanceNamesMissingBases builds two images on a base that was
// deleted before strata ran: their shared layers have no image, so they
// group under a phantom, and their provenance names it.
func TestProvenanceNamesMissingBases(t *testing.T) {
	t.Parallel()
	bases := testreg.Start(t)
	apps := testreg.Start(t)
	when := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	gone := bases.Layered("debian", "py", when, nil, "RUN install python 3.12")
	bases.Push("base/python:3.12", gone)
	bases.Delete("base/python@" + bases.Digest("py"))
	builder := bases.Layered("debian", "builder", when, nil, "RUN install compilers")
	bases.Push("base/builder:1", builder)
	for _, short := range []string{"svc-a", "svc-b"} {
		img := bases.Layered("py", short, when, nil, "COPY app "+short)
		apps.PushIndex("team/"+short+":v1", apps.WithProvenance(img, map[string]string{
			"pkg:docker/" + bases.Host + "/base/python@3.12": bases.Digest("py"),
			"pkg:docker/" + bases.Host + "/base/builder@1":   bases.Digest("builder"),
		}))
	}
	svc := New(fixture.Open(t, nil))
	if _, err := svc.Index(ctx, crawl.Options{
		Registry: apps.Host, Repos: []string{"team/svc-a", "team/svc-b"}, Insecure: true, Keychain: authn.NewMultiKeychain(),
		Provenance: true, FetchBases: true,
	}); err != nil {
		t.Fatal(err)
	}
	ex, err := svc.Explain(ctx, apps.Host+"/team/svc-a:v1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Lineage.Ancestry == nil || ex.Lineage.Ancestry.Node(ex.Lineage.Parent) == nil {
		t.Fatalf("lineage = %+v", ex.Lineage)
	}
	if got := ex.Lineage.Ancestry.Node(ex.Lineage.Parent).Label; got != bases.Host+"/base/python:3.12" {
		t.Fatalf("phantom parent label = %q", got)
	}
	status := map[string]string{}
	for _, b := range ex.Lineage.BuiltFrom {
		status[b.Ref] = b.Status
		if b.Status == "missing" && b.Reason == "" {
			t.Fatalf("missing base without a reason: %+v", b)
		}
	}
	if status[bases.Host+"/base/python:3.12"] != "missing" || status[bases.Host+"/base/builder:1"] != "stage" {
		t.Fatalf("built from = %+v", ex.Lineage.BuiltFrom)
	}
	// The outline names the phantom the same way. It sits under the
	// debian layers, which the builder image shares too.
	out, err := svc.Outline(ctx, "", false, 0, 10)
	if err != nil || len(out.Rows) != 1 {
		t.Fatalf("outline roots = %+v, %v", out, err)
	}
	out, err = svc.Outline(ctx, out.Rows[0].ID, false, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range out.Rows {
		names = append(names, r.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "base/python:3.12") {
		t.Fatalf("outline roots = %v", names)
	}
}
