# Graphs

Lineage answers can be drawn as well as listed. `strata ancestry`, `strata descendants` and `strata query --graph` take `-o` with these formats; rendering happens inside the binary, with no Graphviz needed.

| Format | Rendered with | Best for |
| --- | --- | --- |
| `tree` | the terminal, with color | a quick look at a chain |
| `json` | | your own tooling |
| `mermaid` | plain text | GitHub PRs and READMEs, which render it inline |
| `dot` | plain text | existing Graphviz pipelines |
| `svg` | D2, embedded as a Go library | docs, tickets, incident reports |
| `html` | one self-contained file with Cytoscape.js and ELK, opened offline | exploring: pan, zoom, filter by verdict, click a node for its tags and reasons |

```sh
strata ancestry reg.example.com/payments/api:1.8.2 -o svg > chain.svg
strata descendants reg.example.com/base/go:1.22 --depth 2 -o html > go-1.22.html
strata query 'label.team = "payments" and stale = "stale"' --graph -o html > stale.html
strata map > registry-map.html
```

## Views

- **Ancestry chain:** one path from the image down to its root, top to bottom, with conflicting declared parents drawn in red.
- **Descendant tree:** fans out left to right from a base. This is what a rebuild or deprecation would touch.
- **Query graph:** the images a filter matched, highlighted, plus their ancestry up to shared roots, so you can see where results cluster.
- **Registry map** (`strata map`): an icicle chart with root bases on the left and descendants to the right, sized by image count and shaded by stale share.

## Visual encoding

- Node color is the verdict: fresh, drifting, stale or abandoned.
- Node shape separates a regular image, an external root, a phantom ancestor not in the registry, and a group of collapsed siblings.
- A solid edge comes from layers. A dashed edge comes from a label or annotation only. A red edge marks a conflict between declared and inferred parents.
- Edges found from layers are unlabeled; declared and conflicting edges say where they came from.
- When every node shares one registry host, visual formats leave it out of node names.

## Node budget

An ancestry chain is always short, but the descendants of a popular base can run to a hundred thousand images. Graph output has a node budget, 500 by default:

1. Sibling leaves in the same repo collapse into one node, such as `payments/api ×1,284`.
2. If that is not enough, the deepest nodes are cut and each parent that lost children gets a `+N more` node.
3. `--depth` caps how far down a descendant walk goes, and `--budget N` raises the limit up to 20,000.

The static HTML explorer is for subgraphs that fit the budget. For whole-registry browsing, use `strata serve`.
