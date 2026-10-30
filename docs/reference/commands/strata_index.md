## strata index

Crawl a registry and build or update the index

### Synopsis

Crawl repos, tags, manifests and image configs, then recompute lineage, staleness
and today's snapshot. The crawl is resumable: if it stops, the next run skips the
repos it already finished. Re-crawls only list tags and compare digests, and every
tag move is recorded in tag history from the first crawl on.

Registries come from --registry or from the registries list in the config file.

```
strata index [flags]
```

### Examples

```
  strata index --registry reg.io --workers 64
  strata index --registry localhost:5000 --insecure --repo payments/api
  strata index --external docker.io/library/debian:bookworm-slim
```

### Options

```
      --external strings   external base refs to fetch so roots resolve (repeatable)
  -h, --help               help for index
      --insecure           allow plain HTTP
  -o, --output string      output format: text or json (default "text")
      --rate float         request limit per second (0 means unlimited)
      --referrers          fetch SBOMs, signatures and attestations (default true)
      --registry string    registry host to crawl, such as reg.io (overrides the config's registries)
      --repo strings       crawl only these repos (repeatable); default is the registry catalog
      --workers int        concurrent requests (default 16)
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

