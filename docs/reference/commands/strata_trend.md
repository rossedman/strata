## strata trend

Chart a snapshot metric over time

### Synopsis

Render a daily snapshot metric over time as a self-contained HTML line chart, or
as rows. Metrics: repos, tags, manifests, unique_stacks, untagged_pct,
apparent_bytes, dedup_bytes, exclusive_bytes, shared_cost_bytes, fresh, drifting,
stale, abandoned.

```
strata trend <metric> [flags]
```

### Examples

```
  strata trend dedup_bytes -o html > storage.html
  strata trend stale --group-by root -o csv
```

### Options

```
      --group-by string   one line per group: repo, root, or a required label such as label.team
  -h, --help              help for trend
  -o, --output string     output format: html, table, json, csv or ndjson (default "html")
```

### Options inherited from parent commands

```
      --config string      config file (default: $XDG_CONFIG_HOME/strata/strata.yaml, ~/.strata.yaml or ./strata.yaml)
      --db string          index file (default: $XDG_DATA_HOME/strata/strata.duckdb)
      --log-level string   log level: debug, info, warn or error (default: info)
      --platform string    platform to pick when a ref names a multi-platform index (default: linux/amd64)
      --server string      URL of a running strata serve to query instead of the index file
```

### SEE ALSO

* [strata](strata.md)	 - Turn a container registry into a queryable lineage graph

