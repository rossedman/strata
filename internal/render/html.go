package render

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/rossedman/strata/internal/graph"
)

var (
	//go:embed assets/graph.html
	graphPage string
	//go:embed assets/map.html
	mapPage string
	//go:embed assets/vendor/cytoscape.min.js
	cytoscapeJS string
	//go:embed assets/vendor/elk.bundled.js
	elkJS string
	//go:embed assets/vendor/cytoscape-elk.js
	cytoscapeElkJS string
)

// fill substitutes a page's title, data and vendored scripts.
func fill(page, title string, data any, vendor string) (string, error) {
	js, err := json.Marshal(data) // escapes <, > and &, so it is safe inside <script>
	if err != nil {
		return "", err
	}
	r := strings.NewReplacer(
		"__TITLE__", html.EscapeString(title),
		"/*__DATA__*/", string(js),
		"/*__VENDOR__*/", vendor,
	)
	return r.Replace(page), nil
}

// HTMLTo writes a self-contained interactive explorer: the graph JSON plus
// Cytoscape.js with an ELK layout, opened offline in any browser.
func HTMLTo(w io.Writer, g *graph.Graph) error {
	title := "strata · " + g.Title
	page, err := fill(graphPage, title, g, cytoscapeJS+"\n"+elkJS+"\n"+cytoscapeElkJS)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, page)
	return err
}

// MapTo writes the registry map: an icicle chart with root bases at the
// left and descendants to the right, each block sized by image count and
// shaded by its share of stale images.
func MapTo(w io.Writer, g *graph.Graph) error {
	page, err := fill(mapPage, "strata · registry map", g, "")
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, page)
	return err
}

// Series is one line of a trend chart.
type Series struct {
	Name   string
	Days   []string
	Values []float64
}

// palette colors trend lines; the brand violet comes first.
var palette = []string{"#a99bff", "#5fd39a", "#e8c84a", "#f08a5d", "#6cc6ff", "#ff8fc7", "#c9c4dd", "#7fe0d0"}

// TrendTo writes a snapshot metric as a self-contained HTML line chart,
// drawn as inline SVG with no scripts.
func TrendTo(w io.Writer, metric string, series []Series) error {
	days := map[string]bool{}
	maxV := 0.0
	for _, s := range series {
		for i, d := range s.Days {
			days[d] = true
			maxV = math.Max(maxV, s.Values[i])
		}
	}
	axis := make([]string, 0, len(days))
	for d := range days {
		axis = append(axis, d)
	}
	sort.Strings(axis)
	pos := map[string]int{}
	for i, d := range axis {
		pos[d] = i
	}
	const W, H, L, R, T, B = 960.0, 420.0, 70.0, 20.0, 20.0, 40.0
	x := func(i int) float64 {
		if len(axis) < 2 {
			return L + (W-L-R)/2
		}
		return L + float64(i)*(W-L-R)/float64(len(axis)-1)
	}
	if maxV == 0 {
		maxV = 1
	}
	y := func(v float64) float64 { return T + (H-T-B)*(1-v/maxV) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="%s over time">`, W, H, html.EscapeString(metric))
	for i := 0; i <= 4; i++ {
		v := maxV * float64(i) / 4
		fmt.Fprintf(&b, `<line x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f" stroke="#2a2540"/><text x="%.0f" y="%.1f" text-anchor="end" fill="#8e8aa6" font-size="11">%s</text>`,
			L, W-R, y(v), y(v), L-8, y(v)+4, html.EscapeString(axisLabel(metric, v)))
	}
	for i, d := range axis {
		if len(axis) <= 12 || i%(len(axis)/8+1) == 0 || i == len(axis)-1 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle" fill="#8e8aa6" font-size="11">%s</text>`, x(i), H-14, d)
		}
	}
	for si, s := range series {
		c := palette[si%len(palette)]
		var pts []string
		for i, d := range s.Days {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", x(pos[d]), y(s.Values[i])))
		}
		fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2" points="%s"/>`, c, strings.Join(pts, " "))
		for i, d := range s.Days {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3" fill="%s"><title>%s %s: %s</title></circle>`,
				x(pos[d]), y(s.Values[i]), c, html.EscapeString(s.Name), d, html.EscapeString(axisLabel(metric, s.Values[i])))
		}
	}
	b.WriteString("</svg>")
	var legend strings.Builder
	for si, s := range series {
		fmt.Fprintf(&legend, `<span><i style="background:%s"></i>%s</span>`, palette[si%len(palette)], html.EscapeString(s.Name))
	}
	page := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="generator" content="strata"><title>strata · ` + html.EscapeString(metric) + `</title>
<style>:root{--ink:#0e0d16;--ink-2:#17152a;--ink-3:#2a2540;--brand:#a99bff;--cream:#f4f1ff;--mist:#c9c4dd;--dim:#8e8aa6}
html,body{margin:0;background:var(--ink);color:var(--mist);font:14px/1.5 Inter,ui-sans-serif,system-ui,sans-serif}
main{max-width:1000px;margin:0 auto;padding:24px 16px}h1{margin:0 0 4px;font-size:20px;color:var(--cream);letter-spacing:-.01em}h1 span{color:var(--brand)}
p{margin:0 0 16px;color:var(--dim)}svg{width:100%;height:auto;background:var(--ink-2);border:1px solid var(--ink-3);border-radius:10px}
.legend{display:flex;flex-wrap:wrap;gap:14px;margin-top:12px;font-size:13px}.legend i{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px}</style>
</head><body><main><h1><span>strata</span> · ` + html.EscapeString(metric) + `</h1><p>Daily snapshots, ` + fmt.Sprint(len(axis)) + ` days, ` + fmt.Sprint(len(series)) + ` series.</p>` +
		b.String() + `<div class="legend">` + legend.String() + `</div></main></body></html>
`
	_, err := io.WriteString(w, page)
	return err
}

// axisLabel formats a metric value: bytes for byte metrics, otherwise a
// compact number.
func axisLabel(metric string, v float64) string {
	if strings.HasSuffix(metric, "bytes") {
		units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
		i := 0
		for v >= 1024 && i < len(units)-1 {
			v /= 1024
			i++
		}
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}
