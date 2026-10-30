## strata layers bloat

List big layers that nothing else reuses, with the command behind them

```
strata layers bloat [flags]
```

### Examples

```
  strata layers bloat
  strata layers bloat --group-by repo --min-size 500MB
```

### Options

```
      --group-by string   repo: layers used by one repo only (default: by one image only)
  -h, --help              help for bloat
      --limit int         number of layers (default 20)
      --min-size string   smallest layer to report (default "100MB")
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

* [strata layers](strata_layers.md)	 - Analyze layers: what is most shared and where the bytes go

