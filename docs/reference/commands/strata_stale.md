## strata stale

Report the staleness mix, optionally per group

### Synopsis

Count images per verdict: fresh, drifting, stale and abandoned. Verdicts come from
the policy file; see strata explain for the reasons behind any one image.

```
strata stale [flags]
```

### Examples

```
  strata stale
  strata stale --group-by label.team
  strata stale --group-by root -o csv
```

### Options

```
      --group-by string   group by repo, root or any field such as label.team
  -h, --help              help for stale
  -o, --output string     output format: table, json, csv or ndjson (default "table")
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

