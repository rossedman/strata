## strata explain

Show what an image is, where it came from and whether it is stale

### Synopsis

Show an image's labels, config, layers with the build step behind each, referrers,
its parent chain with the evidence for each edge, and its staleness verdict with
reasons. A ref is repo:tag, repo@digest, a digest, or a digest prefix; the registry
host can be left off when the rest is unambiguous.

```
strata explain <ref> [flags]
```

### Examples

```
  strata explain reg.io/payments/api:1.8.2
  strata explain payments/api:1.8.2 --history
  strata explain sha256:9f2c41ab07e2 -o json
```

### Options

```
  -h, --help            help for explain
      --history         include build steps reconstructed from the config history
  -o, --output string   output format: text or json (default "text")
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

