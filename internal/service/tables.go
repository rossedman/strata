package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/query"
)

// Table is a tabular answer: column names and rows of values.
type Table struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// QueryOptions shape a filter query's output.
type QueryOptions struct {
	Filter string
	// Fields are the columns to return; empty means DefaultFields.
	Fields []string
	// Sort lists fields to order by; a leading - sorts descending.
	Sort []string
	// Limit caps rows; 0 means no limit.
	Limit int
	// GroupBy counts images per value of a field instead of listing them.
	GroupBy string
}

// DefaultFields are the columns a query returns when none are asked for.
var DefaultFields = []string{"repo", "tag", "digest", "platform", "created", "size", "stale"}

// Query runs a filter and collects the result.
func (s *Service) Query(ctx context.Context, opts QueryOptions) (*Table, error) {
	t := &Table{Rows: [][]any{}}
	err := s.QueryEach(ctx, opts, func(cols []string) error {
		t.Columns = cols
		return nil
	}, func(row []any) error {
		t.Rows = append(t.Rows, row)
		return nil
	})
	return t, err
}

// QueryEach runs a filter and streams rows to emit, so a full-registry
// export never buffers. header is called once with the column names.
func (s *Service) QueryEach(ctx context.Context, opts QueryOptions, header func([]string) error, emit func([]any) error) error {
	sqlText, args, cols, err := buildQuery(opts, s.view())
	if err != nil {
		return input(err)
	}
	if err := header(cols); err != nil {
		return err
	}
	return s.each(ctx, sqlText, args, len(cols), emit)
}

// each runs a query and passes every row, normalized, to emit.
func (s *Service) each(ctx context.Context, q string, args []any, n int, emit func([]any) error) error {
	rows, err := s.db().QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("run query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		vals := make([]any, n)
		ptrs := make([]any, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, v := range vals {
			vals[i] = normalize(v)
		}
		if err := emit(vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

// normalize converts driver values into plain JSON-friendly values.
func normalize(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case time.Time:
		return x.UTC()
	case []byte:
		return string(x)
	case int32:
		return int64(x)
	}
	return v
}

// buildQuery compiles query options to SQL over view, with arguments
// and column names.
func buildQuery(opts QueryOptions, view string) (string, []any, []string, error) {
	c, err := query.Compile(opts.Filter)
	if err != nil {
		return "", nil, nil, err
	}
	if opts.GroupBy != "" {
		expr, err := query.Expr(opts.GroupBy)
		if err != nil {
			return "", nil, nil, err
		}
		inner := expr
		if query.IsList(opts.GroupBy) {
			inner = "unnest(" + expr + ")"
		}
		q := fmt.Sprintf(`SELECT g AS %s, count(*) AS images FROM (SELECT %s AS g FROM %s WHERE %s) GROUP BY ALL ORDER BY images DESC, g NULLS LAST`,
			ident(opts.GroupBy), inner, view, c.Where)
		if opts.Limit > 0 {
			q += fmt.Sprintf(" LIMIT %d", opts.Limit)
		}
		return q, c.Args, []string{opts.GroupBy, "images"}, nil
	}
	fields := opts.Fields
	if len(fields) == 0 {
		fields = DefaultFields
	}
	sel := make([]string, len(fields))
	for i, f := range fields {
		expr, err := query.Expr(f)
		if err != nil {
			return "", nil, nil, err
		}
		sel[i] = expr + " AS " + ident(f)
	}
	order := []string{}
	for _, srt := range opts.Sort {
		dir := "ASC"
		if strings.HasPrefix(srt, "-") {
			dir, srt = "DESC", srt[1:]
		}
		expr, err := query.Expr(srt)
		if err != nil {
			return "", nil, nil, err
		}
		order = append(order, expr+" "+dir+" NULLS LAST")
	}
	order = append(order, "repo", "digest")
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + view + " WHERE " + c.Where + " ORDER BY " + strings.Join(order, ", ")
	if opts.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", opts.Limit)
	}
	return q, c.Args, fields, nil
}

// ident quotes a column alias.
func ident(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// groupExpr returns the SQL expression for a summary or staleness
// grouping: repo, root, or any query field such as label.team.
func groupExpr(field string) (string, error) {
	switch field {
	case "":
		return "'all'", nil
	case "root":
		// The root's first tag ref, else repo@digest for an untagged root,
		// else the bare id (a phantom ancestor).
		return "coalesce(list_sort(list_filter(root_refs, lambda x: x NOT LIKE '%@%' AND x NOT LIKE 'sha256:%' AND x NOT LIKE 'phantom:%'))[1], " +
			"list_filter(root_refs, lambda x: x LIKE '%@sha256:%')[1], root, digest)", nil
	}
	if query.IsList(field) {
		return "", fmt.Errorf("cannot group by %s: it has several values per image", field)
	}
	return query.Expr(field)
}

// Stale reports the staleness mix, optionally per group.
func (s *Service) Stale(ctx context.Context, groupBy string) (*Table, error) {
	g, err := groupExpr(groupBy)
	if err != nil {
		return nil, input(err)
	}
	label := groupBy
	if label == "" {
		label = "scope"
	}
	q := `SELECT ` + g + ` AS grp, count(*) AS images,
			count(*) FILTER (WHERE stale = 'fresh') AS fresh,
			count(*) FILTER (WHERE stale = 'drifting') AS drifting,
			count(*) FILTER (WHERE stale = 'stale') AS stale,
			count(*) FILTER (WHERE stale = 'abandoned') AS abandoned,
			round(100.0 * count(*) FILTER (WHERE stale IN ('stale', 'abandoned')) / count(*), 1) AS stale_pct
		FROM ` + s.view() + ` GROUP BY ALL ORDER BY stale_pct DESC, images DESC, grp NULLS LAST`
	t := &Table{Columns: []string{label, "images", "fresh", "drifting", "stale", "abandoned", "stale_pct"}, Rows: [][]any{}}
	err = s.each(ctx, q, nil, len(t.Columns), func(r []any) error {
		t.Rows = append(t.Rows, r)
		return nil
	})
	return t, err
}

// Root is one root base and how much is built on it.
type Root struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Kind        string  `json:"kind"`
	Children    int     `json:"children"`
	Descendants int     `json:"descendants"`
	StalePct    float64 `json:"stale_pct"`
}

// Roots lists every root base with descendants, most-used first.
func (s *Service) Roots(ctx context.Context) ([]Root, error) {
	rows, err := s.db().QueryContext(ctx, `
		SELECT l.digest, l.children, l.descendants,
			coalesce((SELECT round(100.0 * count(*) FILTER (WHERE st.verdict IN ('stale', 'abandoned')) / nullif(count(*), 0), 1)
			 FROM lineage d JOIN staleness st ON st.digest = d.digest WHERE d.root = l.digest AND d.digest <> l.digest), 0)
		FROM lineage l WHERE l.parent IS NULL AND l.descendants > 0
		ORDER BY l.descendants DESC, l.digest`)
	if err != nil {
		return nil, fmt.Errorf("read roots: %w", err)
	}
	var out []Root
	var ids []string
	for rows.Next() {
		var r Root
		var pct sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.Children, &r.Descendants, &pct); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.StalePct = pct.Float64
		out = append(out, r)
		ids = append(ids, r.ID)
	}
	_ = rows.Close()
	nodes, err := s.nodes(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		n := nodes[out[i].ID]
		out[i].Label, out[i].Kind = n.Label, n.Kind
	}
	return out, nil
}

// RootsGraph returns every root and its whole subtree as one graph, for
// the registry map.
func (s *Service) RootsGraph(ctx context.Context) (*graph.Graph, error) {
	rows, err := s.db().QueryContext(ctx, `SELECT digest, parent, parent_source, generation FROM lineage
		WHERE root IN (SELECT digest FROM lineage WHERE parent IS NULL AND descendants > 0)`)
	if err != nil {
		return nil, fmt.Errorf("read map: %w", err)
	}
	depth := map[string]int{}
	var edges []graph.Edge
	for rows.Next() {
		var id string
		var parent, source sql.NullString
		var gen int
		if err := rows.Scan(&id, &parent, &source, &gen); err != nil {
			_ = rows.Close()
			return nil, err
		}
		depth[id] = gen
		if parent.Valid {
			edges = append(edges, graph.Edge{Parent: parent.String, Child: id, Source: source.String})
		}
	}
	_ = rows.Close()
	return s.build(ctx, "map", "", "registry map", depth, edges)
}
