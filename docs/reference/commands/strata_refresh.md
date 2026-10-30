## strata refresh

Recompute lineage, staleness and today's snapshot without crawling

### Synopsis

Recompute the derived views from what is already indexed. Run it after changing
the staleness policy, lineage labels or required labels.

```
strata refresh [flags]
```

### Options

```
  -h, --help   help for refresh
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

