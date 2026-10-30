## strata map

Draw every root base and its sprawl as an icicle chart

### Synopsis

Write the registry map: a self-contained HTML icicle chart with root bases at the
left and their descendants to the right. Each block is sized by image count and
shaded by its share of stale images, so base sprawl is visible at a glance. Click a
block to zoom into it.

```
strata map [flags]
```

### Examples

```
  strata map -o html > registry-map.html
```

### Options

```
  -h, --help            help for map
  -o, --output string   output format: html or json (default "html")
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

