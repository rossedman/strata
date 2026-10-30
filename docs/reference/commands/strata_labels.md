## strata labels

Profile label and annotation keys: coverage, values and near-duplicates

### Synopsis

Without a key, list every label and annotation key with its coverage, number of
distinct values and top values, which separates enums like env from free text like
commit. With a key, show its value counts, the repos lacking it, and when it was
first and last seen. --similar finds near-duplicate keys (team, Team, team_name, and
synonyms from the config) so they can be unified at the source; strata never
rewrites anything.

```
strata labels [key] [flags]
```

### Examples

```
  strata labels
  strata labels team
  strata labels --similar
```

### Options

```
  -h, --help            help for labels
  -o, --output string   output format: table, json, csv or ndjson (default "table")
      --similar         list near-duplicate keys
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

