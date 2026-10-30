## strata ancestry

Walk an image's ancestry back to its root base

### Synopsis

Print the chain from an image to its root base. Each edge is labeled with how the
parent was found: layers (exact prefix match), annotation or label (declared).
A declared parent that disagrees with the layers is shown as a conflict.

```
strata ancestry <ref> [flags]
```

### Examples

```
  strata ancestry reg.io/payments/api:1.8.2
  strata ancestry reg.io/payments/api@sha256:9f2c... -o svg > chain.svg
  strata ancestry payments/api:1.8.2 -o mermaid
```

### Options

```
  -h, --help            help for ancestry
  -o, --output string   output format: tree, json, mermaid, dot, svg or html (default "tree")
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

