## strata roots

List every root base and how much is built on it

### Synopsis

List the root bases images descend from: images with no parent, external bases,
and shared prefixes no indexed image ends at (shown as not in the registry).

```
strata roots [flags]
```

### Examples

```
  strata roots
  strata roots --count-descendants
  strata roots -o json
```

### Options

```
      --count-descendants   print a total line: distinct bases and their descendants
  -h, --help                help for roots
  -o, --output string       output format: table, json, csv or ndjson (default "table")
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

