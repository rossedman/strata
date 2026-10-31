// Package mcpserver exposes strata's questions as Model Context Protocol
// tools, so an agent can ask "which prod images are on an EOL base?" and
// get an answer from the index directly.
package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/service"
)

// RefInput names one image.
type RefInput struct {
	Ref      string `json:"ref" jsonschema:"repo:tag, repo@digest, a digest, or a digest prefix of at least 12 hex characters"`
	Platform string `json:"platform,omitempty" jsonschema:"platform to pick when the ref names a multi-platform index, such as linux/arm64"`
}

// ExplainInput asks about one image.
type ExplainInput struct {
	RefInput
	History bool `json:"history,omitempty" jsonschema:"include the build steps from the config history"`
}

// DescendantsInput asks what is built on a base.
type DescendantsInput struct {
	RefInput
	Depth       int  `json:"depth,omitempty" jsonschema:"levels below the base; 0 means the whole subtree (default 0)"`
	AllVersions bool `json:"all_versions,omitempty" jsonschema:"match every digest the base tag has ever held"`
}

// QueryInput runs a filter.
type QueryInput struct {
	Filter  string   `json:"filter" jsonschema:"filter in strata's query language, such as label.env = \"prod\" and stale.eol_base"`
	Fields  []string `json:"fields,omitempty" jsonschema:"columns to return, such as repo, tag, digest, stale, label.team"`
	Sort    []string `json:"sort,omitempty" jsonschema:"fields to sort by; prefix - for descending"`
	Limit   int      `json:"limit,omitempty" jsonschema:"maximum rows (default 200)"`
	GroupBy string   `json:"group_by,omitempty" jsonschema:"count images per value of this field instead of listing them"`
}

// StaleInput asks for the staleness mix.
type StaleInput struct {
	GroupBy string `json:"group_by,omitempty" jsonschema:"group by repo, root or a field such as label.team"`
}

// CleanupInput asks which images can be deleted.
type CleanupInput struct {
	Where string `json:"where,omitempty" jsonschema:"select candidates with the query language; default stale = \"abandoned\""`
	Scope string `json:"scope,omitempty" jsonschema:"registry (default) counts a layer as freed when no kept image anywhere uses it; repo when none in the same repo does"`
	Limit int    `json:"limit,omitempty" jsonschema:"most candidates to list, largest first (default 50); totals count them all"`
}

// TableOutput is a tabular answer.
type TableOutput struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// GraphOutput wraps a lineage graph.
type GraphOutput struct {
	Graph *graph.Graph `json:"graph"`
}

// RootsOutput lists root bases.
type RootsOutput struct {
	Roots []service.Root `json:"roots"`
}

// queryHelp is the tool description for query, with the language summary
// an agent needs to write filters.
const queryHelp = `Filter indexed images with strata's query language, compiled to SQL.
Combine predicates with and, or, not and parentheses. Operators: = != < <= > >= ~ (glob) in [...] exists.
A field alone is a condition (has:sbom, stale, label.deprecated). ancestor("<ref>") is true if the ref is in the ancestry.
Fields: repo, tag, ref, digest, created, age (180d), size (512MB), layers, platform, os, arch, external,
parent, root (compared against refs), descendants, children, tags, generation, conflict, has:sbom,
has:signature, has:attestation, stale (fresh|drifting|stale|abandoned), stale.base_drift, stale.superseded,
stale.untagged, stale.eol_base, stale.label_policy, config.user, config.entrypoint, config.cmd, config.ports,
config.env_keys, config.workdir, config.healthcheck, label.<key>, missing:<key>.
Example: label.env = "prod" and stale.eol_base`

// New returns an MCP server with tools for explain, ancestry,
// descendants, query, stale, roots and cleanup.
func New(svc *service.Service, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "strata", Version: version}, nil)
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "explain", Annotations: ro,
		Description: "What an image is and where it came from: labels, config, layers, referrers, its parent chain with evidence, and its staleness verdict with reasons."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ExplainInput) (*mcp.CallToolResult, *service.Explanation, error) {
			ex, err := svc.Explain(ctx, in.Ref, in.Platform, in.History)
			return nil, ex, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "ancestry", Annotations: ro,
		Description: "Walk an image's ancestry back to its root base. Edges carry their source: layers (exact), annotation or label (declared)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in RefInput) (*mcp.CallToolResult, GraphOutput, error) {
			g, err := svc.Ancestry(ctx, in.Ref, in.Platform)
			return nil, GraphOutput{Graph: g}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "descendants", Annotations: ro,
		Description: "Everything built on a base image, found by layer prefix whatever it is named. all_versions includes images built on earlier digests of the base tag."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in DescendantsInput) (*mcp.CallToolResult, *service.DescendantResult, error) {
			res, err := svc.Descendants(ctx, in.Ref, service.DescendantOptions{Platform: in.Platform, Depth: in.Depth, AllVersions: in.AllVersions})
			return nil, res, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "query", Annotations: ro, Description: queryHelp},
		func(ctx context.Context, _ *mcp.CallToolRequest, in QueryInput) (*mcp.CallToolResult, TableOutput, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 200
			}
			t, err := svc.Query(ctx, service.QueryOptions{Filter: in.Filter, Fields: in.Fields, Sort: in.Sort, Limit: limit, GroupBy: in.GroupBy})
			if err != nil {
				return nil, TableOutput{}, err
			}
			return nil, TableOutput{Columns: t.Columns, Rows: t.Rows}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "stale", Annotations: ro,
		Description: "Staleness mix: counts of fresh, drifting, stale and abandoned images, optionally per group such as label.team or root."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in StaleInput) (*mcp.CallToolResult, TableOutput, error) {
			t, err := svc.Stale(ctx, in.GroupBy)
			if err != nil {
				return nil, TableOutput{}, err
			}
			return nil, TableOutput{Columns: t.Columns, Rows: t.Rows}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "roots", Annotations: ro,
		Description: "Every root base with descendants, most-used first, with its share of stale descendants."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, RootsOutput, error) {
			roots, err := svc.Roots(ctx)
			if roots == nil {
				roots = []service.Root{}
			}
			return nil, RootsOutput{Roots: roots}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "cleanup", Annotations: ro,
		Description: "Deletion candidates (abandoned images by default, whole multi-platform indexes only, never a base a kept image is built on) and the storage deleting them frees, counting layers shared with kept images as not freed. Nothing is deleted."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in CleanupInput) (*mcp.CallToolResult, *service.Cleanup, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 50
			}
			c, err := svc.Cleanup(ctx, service.CleanupOptions{Where: in.Where, Scope: in.Scope, Limit: limit})
			return nil, c, err
		})
	return s
}
