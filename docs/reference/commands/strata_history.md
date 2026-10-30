## strata history

Export or import tag history, the one part of the index that cannot be rebuilt

### Synopsis

Registries only expose where a tag points now. strata records every tag move from
its first crawl on, which is what base-drift detection depends on. The rest of the
index can be rebuilt from the registry; tag history cannot, so keep a copy.

### Options

```
  -h, --help   help for history
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
* [strata history export](strata_history_export.md)	 - Write tag history to a Parquet file
* [strata history import](strata_history_import.md)	 - Merge tag history from a Parquet file written by history export

