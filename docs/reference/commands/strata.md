## strata

Turn a container registry into a queryable lineage graph

### Synopsis

strata indexes every repo, tag, manifest and image config in a registry into a
local DuckDB file, then answers three questions about any image:

  What is it?            labels, config, layers, size and referrers
  Where did it come from? its ancestry, walked back to the root base
  Is it stale?           an explainable verdict with the reasons spelled out

It needs only registry read access: no source repos, no CI, no database server.

### Examples

```
  strata index --registry reg.io
  strata explain reg.io/payments/api:1.8.2
  strata descendants reg.io/base/go:1.22 --all-versions --depth 0
  strata query 'label.team = "payments" and age > 180d'
  strata stale --group-by label.team
```

### Options

```
      --config string      config file (default: $XDG_CONFIG_HOME/strata/strata.yaml, ~/.strata.yaml or ./strata.yaml)
      --db string          index file (default: $XDG_DATA_HOME/strata/strata.duckdb)
  -h, --help               help for strata
      --log-level string   log level: debug, info, warn or error (default: info)
      --platform string    platform to pick when a ref names a multi-platform index (default: linux/amd64)
      --server string      URL of a running strata serve to query instead of the index file
```

### SEE ALSO

* [strata ancestry](strata_ancestry.md)	 - Walk an image's ancestry back to its root base
* [strata bases](strata_bases.md)	 - Analyze base images
* [strata descendants](strata_descendants.md)	 - Find everything built on a base, whatever it is named
* [strata diff](strata_diff.md)	 - Report what changed in the registry since an earlier snapshot
* [strata explain](strata_explain.md)	 - Show what an image is, where it came from and whether it is stale
* [strata history](strata_history.md)	 - Export or import tag history, the one part of the index that cannot be rebuilt
* [strata index](strata_index.md)	 - Crawl a registry and build or update the index
* [strata labels](strata_labels.md)	 - Profile label and annotation keys: coverage, values and near-duplicates
* [strata layers](strata_layers.md)	 - Analyze layers: what is most shared and where the bytes go
* [strata map](strata_map.md)	 - Draw every root base and its sprawl as an icicle chart
* [strata mcp](strata_mcp.md)	 - Run an MCP server over stdio for AI agents
* [strata query](strata_query.md)	 - Filter images with strata's query language
* [strata refresh](strata_refresh.md)	 - Recompute lineage, staleness and today's snapshot without crawling
* [strata roots](strata_roots.md)	 - List every root base and how much is built on it
* [strata serve](strata_serve.md)	 - Serve the HTTP API and an MCP endpoint over the index
* [strata stale](strata_stale.md)	 - Report the staleness mix, optionally per group
* [strata summary](strata_summary.md)	 - Show the shape of the registry in one screen
* [strata trend](strata_trend.md)	 - Chart a snapshot metric over time
* [strata version](strata_version.md)	 - Print the version, commit, build date and index format

