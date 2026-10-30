## strata diff

Report what changed in the registry since an earlier snapshot

### Synopsis

Compare today's snapshot with the one from --since ago: new and removed repos, new
root bases, image count and deduplicated-bytes growth, shifts in the staleness mix,
changes in label coverage, and repos that went dark (no pushes in --dark-days while
still holding tagged images). Snapshots begin on the first index run.

```
strata diff [flags]
```

### Examples

```
  strata diff --since 7d
  strata diff --since 30d --group-by label.team
```

### Options

```
      --dark-days int     days without a push before a tagged repo counts as dark (default 90)
      --group-by string   compare per group: repo, root or a required label such as label.team
  -h, --help              help for diff
  -o, --output string     output format: table or json (default "table")
      --since string      compare with the snapshot this long ago (such as 7d, 4w) (default "7d")
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

