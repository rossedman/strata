## strata layers top

List the most widely shared or the biggest layers

### Synopsis

--by shares shows the real foundations of the registry, which are often different
from the bases people name. --by size shows the biggest layers overall.

```
strata layers top [flags]
```

### Examples

```
  strata layers top --by shares
  strata layers top --by size --limit 50
```

### Options

```
      --by string       rank by shares or size (default "shares")
  -h, --help            help for top
      --limit int       number of layers (default 20)
  -o, --output string   output format: table, json, csv or ndjson (default "table")
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

