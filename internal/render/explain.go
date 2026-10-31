package render

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/ui"
)

// Explain writes a human-readable report of one image.
func Explain(w io.Writer, ex *service.Explanation) error {
	var b strings.Builder
	im := ex.Image
	section := func(title string) { b.WriteString("\n" + ui.Title.Render(title) + "\n") }
	kv := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(&b, "  %s %s\n", ui.Dim.Render(fmt.Sprintf("%-13s", k)), v)
	}

	b.WriteString(ui.Title.Render(im.Label))
	if v := ex.Staleness.Verdict; v != "" {
		b.WriteString("  " + ui.VerdictStyle(v).Render(v))
	}
	b.WriteString("\n" + ui.Dim.Render(im.Digest) + "\n")

	section("Image")
	kv("repo", im.Repo)
	kv("tags", strings.Join(im.Tags, ", "))
	kv("platform", im.Platform)
	if len(im.Platforms) > 1 {
		kv("index", graph.ShortDigest(im.IndexDigest)+" ("+strings.Join(im.Platforms, ", ")+")")
	}
	if im.Created != nil {
		age := ""
		if im.AgeDays != nil {
			age = fmt.Sprintf(" (%d days ago)", *im.AgeDays)
		}
		kv("created", im.Created.Format(time.DateOnly)+age)
	} else {
		kv("created", "unknown (zero timestamp, likely a reproducible build)")
	}
	kv("size", ui.Bytes(im.Size)+fmt.Sprintf(" in %d layers", im.LayerCount))
	if im.External {
		kv("external", "yes, fetched from outside the indexed registries")
	}

	section("Staleness")
	if ex.Staleness.Verdict == "" {
		b.WriteString("  " + ui.Dim.Render("not computed yet; run strata refresh") + "\n")
	}
	for _, r := range ex.Staleness.Reasons {
		b.WriteString("  • " + r + "\n")
	}
	if ex.Staleness.Verdict == "fresh" && len(ex.Staleness.Reasons) == 0 {
		b.WriteString("  " + ui.Dim.Render("on its current base, tagged, and nothing in its labels says otherwise") + "\n")
	}

	section("Lineage")
	kv("status", ex.Lineage.Status)
	kv("generation", fmt.Sprint(ex.Lineage.Generation))
	kv("descendants", fmt.Sprintf("%d (%d direct)", ex.Lineage.Descendants, ex.Lineage.Children))
	if g := ex.Lineage.Ancestry; g != nil && len(g.Nodes) > 1 {
		var tb strings.Builder
		if err := Tree(&tb, g); err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimRight(tb.String(), "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	if len(ex.Lineage.Edges) > 0 {
		b.WriteString("  " + ui.Dim.Render("parent evidence:") + "\n")
		for _, e := range ex.Lineage.Edges {
			name := graph.ShortDigest(e.Parent)
			if ex.Lineage.Ancestry != nil {
				if n := ex.Lineage.Ancestry.Node(e.Parent); n != nil {
					name = n.Label
				}
			}
			line := fmt.Sprintf("    %-11s %s (%s)", e.Source, name, e.Confidence)
			if e.Conflict {
				line = ui.Bad.Render(line + " conflicts with layers")
			}
			b.WriteString(line + "\n")
		}
	}
	if len(ex.Lineage.BuiltFrom) > 0 {
		b.WriteString("  " + ui.Dim.Render("built from, by its provenance:") + "\n")
		for _, bf := range ex.Lineage.BuiltFrom {
			what := map[string]string{
				"parent":  "the parent",
				"stage":   "another build stage; the image does not start with its layers",
				"pending": "not indexed yet; the next strata index fetches it",
				"missing": "not available: " + bf.Reason,
			}[bf.Status]
			line := fmt.Sprintf("    %s  %s", bf.Ref, ui.Dim.Render(what))
			if bf.Status == "missing" {
				line = fmt.Sprintf("    %s  %s", bf.Ref, ui.Bad.Render(what))
			}
			b.WriteString(line + "\n")
		}
	}

	if len(im.Labels) > 0 {
		section("Labels and annotations")
		keys := make([]string, 0, len(im.Labels))
		for k := range im.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s = %s\n", ui.Key.Render(k), im.Labels[k])
		}
	}

	section("Config")
	c := im.Config
	user := c.User
	if user == "" {
		user = "(empty: root)"
	}
	kv("user", user)
	kv("entrypoint", strings.Join(c.Entrypoint, " "))
	kv("cmd", strings.Join(c.Cmd, " "))
	kv("workdir", c.Workdir)
	kv("ports", strings.Join(c.Ports, ", "))
	kv("env keys", strings.Join(c.EnvKeys, ", "))
	kv("healthcheck", fmt.Sprint(c.Healthcheck))
	kv("stop signal", c.StopSignal)

	section("Layers")
	for _, l := range ex.Layers {
		shared := ""
		if l.SharedBy > 1 {
			shared = ui.Dim.Render(fmt.Sprintf(" shared by %d", l.SharedBy))
		}
		cmd := l.CreatedBy
		if len(cmd) > 72 {
			cmd = cmd[:71] + "…"
		}
		fmt.Fprintf(&b, "  %2d %10s%s  %s\n", l.Position, ui.Bytes(l.Size), shared, ui.Dim.Render(cmd))
	}

	if len(ex.History) > 0 {
		section("Build history")
		for i, h := range ex.History {
			mark := "  "
			if h.EmptyLayer {
				mark = ui.Dim.Render("· ")
			}
			fmt.Fprintf(&b, "  %2d %s%s\n", i+1, mark, h.CreatedBy)
		}
	}

	if len(ex.Referrers) > 0 {
		section("Referrers")
		for _, r := range ex.Referrers {
			fmt.Fprintf(&b, "  %-12s %s %s\n", r.Kind, graph.ShortDigest(r.Digest), ui.Dim.Render(r.ArtifactType))
		}
	}

	if len(ex.Findings) > 0 {
		section("Findings")
		for _, f := range ex.Findings {
			b.WriteString("  " + ui.Warning.Render("!") + " " + f + "\n")
		}
	}
	ui.Fprint(w, b.String())
	return nil
}
