## strata summary

Show the shape of the registry in one screen

### Synopsis

Count repos, tags and manifests, distinct layer stacks (how much is just retagging),
the untagged share, platforms, bytes and the staleness mix.

Apparent bytes sum every image's compressed layers as stated in its manifest.
Deduplicated bytes count each blob once, close to real storage. Grouped, exclusive
bytes are layers only that group uses (what removing it would free) and shared-cost
bytes split each shared layer equally among the groups using it (who uses most).

```
strata summary [flags]
```

### Examples

```
  strata summary
  strata summary --group-by label.team
  strata summary --group-by root -o json
```

### Options

```
      --group-by string   group by repo, root or any field such as label.team
  -h, --help              help for summary
  -o, --output string     output format: table, json or csv (default "table")
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

