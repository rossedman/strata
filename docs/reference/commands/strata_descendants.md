## strata descendants

Find everything built on a base, whatever it is named

### Synopsis

Match every image whose layer stack starts with the base's, so descendants are
found even when their names and labels say nothing about the base.

A tag only points at today's digest. --all-versions widens the match to every
digest the tag has held since strata began recording (exact), plus older images
in the base repo created before recording began (inferred). --versions-from adds
digests or refs from a file, one per line (declared).

```
strata descendants <ref> [flags]
```

### Examples

```
  strata descendants reg.io/base/go:1.22
  strata descendants reg.io/base/go:1.22 --depth 0
  strata descendants reg.io/base/go:1.22 --all-versions --depth 0
  strata descendants reg.io/base/go:1.22 --all-versions --group-by base_version
  strata descendants reg.io/base/go:1.22 --depth 2 -o html > go-1.22.html
```

### Options

```
      --all-versions           match every digest the base tag has pointed to
      --budget int             maximum graph nodes; siblings in one repo collapse first (default 500)
      --depth int              levels below the base to include; 0 means the whole subtree (default 1)
      --group-by string        summarize instead of drawing: base_version counts images per base version
  -h, --help                   help for descendants
  -o, --output string          output format: tree, json, mermaid, dot, svg or html (table, csv or ndjson with --group-by) (default "tree")
      --versions-from string   file of extra base digests or refs, one per line
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

