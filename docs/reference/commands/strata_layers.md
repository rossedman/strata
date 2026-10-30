## strata layers

Analyze layers: what is most shared and where the bytes go

### Synopsis

Layer analytics. Each non-empty history entry maps to one layer, so every layer
is traced to the build command that created it.

### Options

```
  -h, --help   help for layers
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
* [strata layers bloat](strata_layers_bloat.md)	 - List big layers that nothing else reuses, with the command behind them
* [strata layers top](strata_layers_top.md)	 - List the most widely shared or the biggest layers

