## strata query

Filter images with strata's query language

### Synopsis

Filter images with a small language that compiles to SQL, so every filter runs
inside DuckDB. Combine predicates with and, or, not and parentheses.

Operators: =  !=  <  <=  >  >=  ~ (glob)  in [...]  exists
A field on its own is a condition: has:sbom, stale, label.deprecated.
Functions: ancestor("<ref>") is true if the ref is anywhere in the ancestry.

Fields:
  age                    days since created (accepts 180d, 12w, 1y)
  arch                   CPU architecture
  children               number of direct children
  config.cmd             cmd arguments
  config.entrypoint      entrypoint arguments
  config.env_keys        environment variable names (values are never stored)
  config.healthcheck     a healthcheck is defined
  config.ports           exposed ports, such as 8080/tcp
  config.stop_signal     stop signal
  config.user            the user the image runs as
  config.workdir         working directory
  conflict               a declared parent disagrees with the layers
  created                creation time from the image config
  descendants            number of images built on this one, at any depth
  digest                 manifest digest
  external               true for bases fetched from outside the indexed registries
  generation             number of ancestors
  has:attestation        an attestation is attached as a referrer
  has:sbom               an SBOM is attached as a referrer
  has:signature          a signature is attached as a referrer
  index                  digest of the multi-platform index holding the image
  label.<key>            any config label or manifest annotation
  layers                 number of layers
  missing:<key>          the label is not set
  os                     operating system
  parent                 the parent image, compared against its refs
  parent_source          how the parent was found: layers, annotation, label
  platform               os/arch[/variant]
  ref                    any repo:tag or repo@digest naming the image
  repo                   repository, including the registry host
  root                   the root base, compared against its refs
  size                   compressed size from the manifest (accepts 512MB, 1.5GiB)
  stale                  verdict: fresh, drifting, stale or abandoned
  stale.base_drift       generations the parent is behind its tag
  stale.base_drift_days  days since the parent was replaced
  stale.base_tag         the tag base drift is measured against
  stale.eol_base         the root base is past end of life
  stale.inferred         a signal was approximated without tag history
  stale.label_policy     labels mark the image deprecated or expired
  stale.last_pulled      days since the last pull
  stale.superseded       days since a tag moved off this image
  stale.untagged         no tag points at the image
  tag                    any tag pointing at the image (name only)
  tags                   number of tags pointing at the image
  variant                CPU variant


```
strata query <filter> [flags]
```

### Examples

```
  strata query 'label.team = "payments" and age > 180d'
  strata query 'ancestor("reg.io/base/go:1.21") and not has:sbom'
  strata query 'stale in ["stale", "abandoned"] and label.env = "prod"'
  strata query 'tags = 0 and descendants = 0' -o ndjson > orphans.ndjson
  strata query 'missing:team' --group-by repo
  strata query 'label.team = "payments" and stale = "stale"' --graph -o html > stale.html
```

### Options

```
      --budget int        maximum graph nodes with --graph (default 500)
      --fields strings    columns to return (default: repo,tag,digest,platform,created,size,stale)
      --graph             draw the matches and their ancestry up to shared roots
      --group-by string   count images per value of a field instead of listing them
  -h, --help              help for query
      --limit int         maximum rows (0 means all)
  -o, --output string     output format: table, json, csv or ndjson (tree, mermaid, dot, svg or html with --graph) (default "table")
      --sort strings      fields to sort by; prefix with - for descending, such as -age
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

