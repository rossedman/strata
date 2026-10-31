package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rossedman/strata/internal/testenv"
)

// connect runs the server over in-memory transports and returns a client
// session.
func connect(t *testing.T) (*mcp.ClientSession, *testenv.Env) {
	t.Helper()
	ctx := context.Background()
	e := testenv.Indexed(t)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := New(e.Svc, "test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, e
}

func TestTools(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cs, e := connect(t)
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not marked read-only", tool.Name)
		}
	}
	if strings.Join(names, ",") != "ancestry,cleanup,descendants,explain,query,roots,stale" {
		t.Fatalf("tools = %v", names)
	}
	calls := []struct {
		name string
		args map[string]any
		want string
	}{
		{"explain", map[string]any{"ref": e.Reg.Host + "/payments/api:1.0", "history": true}, `"repo":"` + e.Reg.Host + `/payments/api"`},
		{"ancestry", map[string]any{"ref": "payments/worker:v1"}, `"kind":"ancestry"`},
		{"descendants", map[string]any{"ref": "base/debian:12", "all_versions": true}, `"versions"`},
		{"query", map[string]any{"filter": `label.team = "payments"`, "fields": []string{"repo"}}, `"columns":["repo"]`},
		{"query", map[string]any{"filter": "", "group_by": "label.team", "limit": 1}, `"images"`},
		{"stale", map[string]any{"group_by": "repo"}, `"stale_pct"`},
		{"roots", map[string]any{}, `"descendants":3`},
		{"cleanup", map[string]any{"where": "tags = 0"}, `"reclaimable_bytes"`},
	}
	for _, c := range calls {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.name, Arguments: c.args})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.IsError {
			t.Fatalf("%s returned an error: %+v", c.name, res.Content)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(b), c.want) {
			t.Errorf("%s output lacks %s: %.300s", c.name, c.want, b)
		}
	}
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"explain", map[string]any{"ref": "nope:1"}},
		{"query", map[string]any{"filter": "nope = 1"}},
		{"stale", map[string]any{"group_by": "tag"}},
		{"cleanup", map[string]any{"scope": "galaxy"}},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.name, Arguments: c.args})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !res.IsError {
			t.Errorf("%s(%v) did not report an error", c.name, c.args)
		}
	}
}
