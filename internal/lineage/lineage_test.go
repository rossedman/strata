package lineage

import (
	"context"
	"database/sql"
	"testing"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/store"
)

// registry is a small registry that exercises every lineage case.
type registry struct {
	debian, goBase, service, api, apiRetag, worker *model.Image
	ph1, ph2, scratch, conflict, cycleA, cycleB    *model.Image
}

// build writes the test registry to an in-memory store and recomputes
// lineage over it.
func build(t *testing.T) (*registry, *sql.DB) {
	t.Helper()
	r := &registry{
		debian:   fixture.Image("debian", "docker.io/library/debian", "d1"),
		goBase:   fixture.Image("go", "reg.io/base/go", "d1", "g1", "g2"),
		service:  fixture.Image("service", "reg.io/base/go-service", "d1", "g1", "g2", "s1"),
		api:      fixture.Created(fixture.Image("api", "reg.io/payments/api", "d1", "g1", "g2", "s1", "a1"), 5),
		apiRetag: fixture.Created(fixture.Image("api-retag", "reg.io/mirror/api", "d1", "g1", "g2", "s1", "a1"), 9),
		worker:   fixture.Image("worker", "reg.io/payments/worker", "d1", "g1", "g2", "s1", "w1"),
		ph1:      fixture.Image("ph1", "reg.io/x/one", "d1", "p1", "p2", "x"),
		ph2:      fixture.Image("ph2", "reg.io/x/two", "d1", "p1", "p2", "y"),
		scratch:  fixture.Image("scratch", "reg.io/x/static", "z1"),
		conflict: fixture.Image("conflict", "reg.io/x/conflict", "d1", "g1", "g2", "s1", "c1"),
		cycleA:   fixture.Image("cycle-a", "reg.io/x/cycle-a", "ca"),
		cycleB:   fixture.Image("cycle-b", "reg.io/x/cycle-b", "cb"),
	}
	r.scratch.Annotations["org.opencontainers.image.base.name"] = "reg.io/base/go:1.22"
	fixture.Label(r.conflict, "base", "reg.io/base/go:1.22")
	fixture.Label(r.cycleA, "base", r.cycleB.Digest)
	fixture.Label(r.cycleB, "base", r.cycleA.Digest)
	b := &store.Batch{
		Images: []*model.Image{r.debian, r.goBase, r.service, r.api, r.apiRetag, r.worker, r.ph1, r.ph2, r.scratch, r.conflict, r.cycleA, r.cycleB},
		Tags:   []model.Tag{fixture.Tag(r.goBase, "1.22", 0), fixture.Tag(r.debian, "bookworm-slim", 0)},
	}
	s := fixture.Open(t, b)
	if _, err := Recompute(context.Background(), s.DB(), Options{Labels: []string{"base"}}); err != nil {
		t.Fatal(err)
	}
	// Recomputing is idempotent.
	if _, err := Recompute(context.Background(), s.DB(), Options{Labels: []string{"base"}}); err != nil {
		t.Fatal(err)
	}
	return r, s.DB()
}

// parent returns the preferred parent recorded for a node.
func parent(t *testing.T, db *sql.DB, id string) (string, string) {
	t.Helper()
	var p, src sql.NullString
	if err := db.QueryRow(`SELECT parent, parent_source FROM lineage WHERE digest = ?`, id).Scan(&p, &src); err != nil {
		t.Fatalf("lineage for %s: %v", id, err)
	}
	return p.String, src.String
}

func TestParents(t *testing.T) {
	t.Parallel()
	r, db := build(t)
	phantom := model.PhantomPrefix + r.ph1.ChainIDs[2]
	tests := []struct {
		name         string
		child        string
		parent, from string
	}{
		{"root has no parent", r.debian.Digest, "", ""},
		{"base on external root", r.goBase.Digest, r.debian.Digest, model.SourceLayers},
		{"intermediate", r.service.Digest, r.goBase.Digest, model.SourceLayers},
		{"leaf", r.api.Digest, r.service.Digest, model.SourceLayers},
		{"retag shares the parent", r.apiRetag.Digest, r.service.Digest, model.SourceLayers},
		{"sibling", r.worker.Digest, r.service.Digest, model.SourceLayers},
		{"deleted ancestor becomes a phantom", r.ph1.Digest, phantom, model.SourceLayers},
		{"phantom links to its own parent", phantom, r.debian.Digest, model.SourceLayers},
		{"scratch image uses its annotation", r.scratch.Digest, r.goBase.Digest, model.SourceAnnotation},
		{"layers win over a conflicting label", r.conflict.Digest, r.service.Digest, model.SourceLayers},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, src := parent(t, db, tt.child)
			if p != tt.parent || src != tt.from {
				t.Fatalf("parent = %s (%s), want %s (%s)", p, src, tt.parent, tt.from)
			}
		})
	}
}

func TestRootsAndCounts(t *testing.T) {
	t.Parallel()
	r, db := build(t)
	var root string
	var gen, children, desc int
	var conflict bool
	q := `SELECT root, generation, children, descendants, conflict FROM lineage WHERE digest = ?`
	if err := db.QueryRow(q, r.api.Digest).Scan(&root, &gen, &children, &desc, &conflict); err != nil {
		t.Fatal(err)
	}
	if root != r.debian.Digest || gen != 3 || conflict {
		t.Fatalf("api: root=%s gen=%d conflict=%v", root, gen, conflict)
	}
	if err := db.QueryRow(q, r.goBase.Digest).Scan(&root, &gen, &children, &desc, &conflict); err != nil {
		t.Fatal(err)
	}
	// go-service and the scratch image are direct children; service has
	// api, the retag, worker and conflict below it.
	if children != 2 || desc != 6 {
		t.Fatalf("go base: children=%d descendants=%d, want 2 and 6", children, desc)
	}
	if err := db.QueryRow(q, r.conflict.Digest).Scan(&root, &gen, &children, &desc, &conflict); err != nil {
		t.Fatal(err)
	}
	if !conflict {
		t.Fatal("conflicting label was not flagged")
	}
	var cycleRoot sql.NullString
	if err := db.QueryRow(`SELECT root FROM lineage WHERE digest = ?`, r.cycleA.Digest).Scan(&cycleRoot); err != nil {
		t.Fatal(err)
	}
	if cycleRoot.Valid {
		t.Fatalf("a declared cycle got root %s", cycleRoot.String)
	}
	var phantoms int
	if err := db.QueryRow(`SELECT count(*) FROM phantoms`).Scan(&phantoms); err != nil || phantoms != 1 {
		t.Fatalf("phantoms = %d, %v", phantoms, err)
	}
}

func TestQuote(t *testing.T) {
	t.Parallel()
	if got := quote("it's"); got != "'it''s'" {
		t.Fatalf("quote = %s", got)
	}
}

func TestRecomputeCanceled(t *testing.T) {
	t.Parallel()
	s := fixture.Open(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Recompute(ctx, s.DB(), Options{}); err == nil {
		t.Fatal("Recompute ran on a canceled context")
	}
}

// TestBuildCacheIsNotABase covers phantoms, layers several stacks share
// that no indexed image ends at. Builds of one repo share their
// dependency layers; that is build cache, not a base, so those builds hang
// off the base above. A repo's only unindexed base, with nothing above
// it, still groups its builds, and layers two repos share stay a node.
func TestBuildCacheIsNotABase(t *testing.T) {
	t.Parallel()
	node := fixture.Image("node", "reg.io/base/node", "n1")
	imgs := []*model.Image{
		node,
		// Two builds of web on node, sharing an npm install layer.
		fixture.Image("web-1", "reg.io/web", "n1", "npm", "code-1"),
		fixture.Image("web-2", "reg.io/web", "n1", "npm", "code-2"),
		// Two builds of tool on a base the index lacks.
		fixture.Image("tool-1", "reg.io/tool", "x1", "x2", "t1"),
		fixture.Image("tool-2", "reg.io/tool", "x1", "x2", "t2"),
		// Two repos on a shared base the index lacks.
		fixture.Image("a", "reg.io/a", "s1", "s2", "a"),
		fixture.Image("b", "reg.io/b", "s1", "s2", "b"),
	}
	s := fixture.Open(t, &store.Batch{Images: imgs})
	ctx := context.Background()
	if _, err := Recompute(ctx, s.DB(), Options{}); err != nil {
		t.Fatal(err)
	}
	parent := func(name string) string {
		var p sql.NullString
		if err := s.DB().QueryRow(`SELECT parent FROM lineage WHERE digest = ?`, fixture.Digest("manifest:"+name)).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p.String
	}
	if got := parent("web-1"); got != node.Digest {
		t.Errorf("web-1's parent = %s, want node: the npm layer is web's build cache", got)
	}
	if got := parent("tool-1"); !model.IsPhantom(got) || got != parent("tool-2") {
		t.Errorf("tool builds = %s and %s, want one phantom base", got, parent("tool-2"))
	}
	if got := parent("a"); !model.IsPhantom(got) || got != parent("b") {
		t.Errorf("a and b = %s and %s, want one shared phantom", got, parent("b"))
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM phantoms`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("phantoms = %d, %v", n, err)
	}
}
