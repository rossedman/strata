## strata history export

Write tag history to a Parquet file

```
strata history export <file.parquet> [flags]
```

### Examples

```
  strata history export tag-history.parquet
```

### Options

```
  -h, --help   help for export
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

* [strata history](strata_history.md)	 - Export or import tag history, the one part of the index that cannot be rebuilt

