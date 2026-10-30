## strata bases similar

Find near-duplicate bases that could be consolidated

### Synopsis

Group bases (images with descendants) whose stacks match except for the last one or
two layers, with an estimate of the deduplicated bytes saved if each cluster were
standardized on one base.

```
strata bases similar [flags]
```

### Options

```
  -h, --help            help for similar
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

* [strata bases](strata_bases.md)	 - Analyze base images

