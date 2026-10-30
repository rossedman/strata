// Package render writes strata's answers as tables, trees, JSON, CSV,
// NDJSON, Mermaid, DOT, SVG and self-contained HTML. Machine formats are
// never styled.
package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/ui"
)

// Formats for tabular output.
const (
	Table  = "table"
	JSON   = "json"
	CSV    = "csv"
	NDJSON = "ndjson"
)

// RowWriter streams rows in one format.
type RowWriter interface {
	// Header is called once with the column names.
	Header(cols []string) error
	// Row writes one row.
	Row(vals []any) error
	// Close flushes buffered output.
	Close() error
}

// NewRowWriter returns a writer for table, json, csv or ndjson.
func NewRowWriter(w io.Writer, format string) (RowWriter, error) {
	switch format {
	case "", Table:
		return &tableWriter{w: w}, nil
	case JSON:
		return &jsonWriter{w: w}, nil
	case CSV:
		return &csvWriter{w: csv.NewWriter(w)}, nil
	case NDJSON:
		return &ndjsonWriter{enc: json.NewEncoder(w)}, nil
	}
	return nil, fmt.Errorf("unknown output format %q (table, json, csv, ndjson)", format)
}

// WriteRows writes a whole table in one format.
func WriteRows(w io.Writer, format string, cols []string, rows [][]any) error {
	rw, err := NewRowWriter(w, format)
	if err != nil {
		return err
	}
	if err := rw.Header(cols); err != nil {
		return err
	}
	for _, r := range rows {
		if err := rw.Row(r); err != nil {
			return err
		}
	}
	return rw.Close()
}

// tableWriter buffers rows and draws a styled table on Close.
type tableWriter struct {
	w    io.Writer
	cols []string
	rows [][]string
}

// Header records the columns.
func (t *tableWriter) Header(cols []string) error {
	t.cols = cols
	return nil
}

// Row formats one row for humans.
func (t *tableWriter) Row(vals []any) error {
	r := make([]string, len(vals))
	for i, v := range vals {
		r[i] = Cell(t.cols[i], v)
	}
	t.rows = append(t.rows, r)
	return nil
}

// Close draws the table.
func (t *tableWriter) Close() error {
	if len(t.rows) == 0 {
		ui.Fprint(t.w, ui.Dim.Render("no results")+"\n")
		return nil
	}
	verdictCol := -1
	for i, c := range t.cols {
		if c == "stale" || c == "verdict" {
			verdictCol = i
		}
	}
	tb := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderHeader(false).BorderColumn(false).
		Headers(t.cols...).
		Rows(t.rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			base := lipgloss.NewStyle().PaddingRight(2)
			if row == table.HeaderRow {
				return base.Inherit(ui.Header)
			}
			if col == verdictCol && row >= 0 && row < len(t.rows) {
				return base.Inherit(ui.VerdictStyle(t.rows[row][col]))
			}
			return base
		})
	ui.Fprint(t.w, tb.Render()+"\n")
	return nil
}

// Cell formats a value for a human-readable table.
func Cell(col string, v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case time.Time:
		if x.Year() < 1980 {
			return "-"
		}
		return x.Format(time.DateOnly)
	case []string:
		if strings.Contains(col, "digest") {
			short := make([]string, len(x))
			for i := range x {
				short[i] = graph.ShortDigest(x[i])
			}
			x = short
		}
		return strings.Join(x, ", ")
	case string:
		if x == "" {
			return "-"
		}
		if strings.HasPrefix(x, "sha256:") || strings.HasPrefix(x, "phantom:") {
			return graph.ShortDigest(strings.TrimPrefix(x, "phantom:"))
		}
		return x
	case int64:
		if strings.Contains(col, "bytes") || col == "size" {
			return ui.Bytes(x)
		}
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

// jsonWriter collects rows into one JSON array of objects.
type jsonWriter struct {
	w    io.Writer
	cols []string
	rows []map[string]any
}

// Header records the columns.
func (j *jsonWriter) Header(cols []string) error {
	j.cols = cols
	j.rows = []map[string]any{}
	return nil
}

// Row adds one object.
func (j *jsonWriter) Row(vals []any) error {
	j.rows = append(j.rows, object(j.cols, vals))
	return nil
}

// Close writes the array.
func (j *jsonWriter) Close() error { return JSONTo(j.w, j.rows) }

// object zips columns and values.
func object(cols []string, vals []any) map[string]any {
	m := make(map[string]any, len(cols))
	for i, c := range cols {
		m[c] = vals[i]
	}
	return m
}

// ndjsonWriter writes one JSON object per line as rows arrive, so a full
// export streams.
type ndjsonWriter struct {
	enc  *json.Encoder
	cols []string
}

// Header records the columns.
func (n *ndjsonWriter) Header(cols []string) error {
	n.cols = cols
	return nil
}

// Row writes one line.
func (n *ndjsonWriter) Row(vals []any) error { return n.enc.Encode(object(n.cols, vals)) }

// Close does nothing; every row is already written.
func (n *ndjsonWriter) Close() error { return nil }

// csvWriter writes RFC 4180 CSV.
type csvWriter struct {
	w *csv.Writer
}

// Header writes the header line.
func (c *csvWriter) Header(cols []string) error { return c.w.Write(cols) }

// Row writes one record; lists are joined with semicolons.
func (c *csvWriter) Row(vals []any) error {
	r := make([]string, len(vals))
	for i, v := range vals {
		switch x := v.(type) {
		case nil:
		case []string:
			r[i] = strings.Join(x, ";")
		case time.Time:
			r[i] = x.Format(time.RFC3339)
		default:
			r[i] = fmt.Sprint(x)
		}
	}
	return c.w.Write(r)
}

// Close flushes the CSV writer.
func (c *csvWriter) Close() error {
	c.w.Flush()
	return c.w.Error()
}

// JSONTo writes v as indented JSON.
func JSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
