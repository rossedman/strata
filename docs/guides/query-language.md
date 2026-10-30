# Query language

`strata query` takes a small filter language. Filters compile to SQL with every value passed as a parameter, so they run inside DuckDB over the whole index instead of in memory.

```sh
strata query 'label.team = "payments" and age > 180d'
strata query 'ancestor("reg.example.com/base/go:1.21") and not has:sbom'
strata query 'stale in ["stale", "abandoned"] and label.env = "prod"'
strata query 'root ~ "docker.io/library/debian:*" and label.deprecated exists'
strata query 'tags = 0 and descendants = 0'
```

## Syntax

| Form | Meaning |
| --- | --- |
| `field = value`, `!=`, `<`, `<=`, `>`, `>=` | Compare. `!=` also matches images where the field is not set. |
| `field ~ "glob"` | Glob match: `*`, `?` and `[...]` |
| `field in ["a", "b"]` | Any of the values |
| `field exists` | The field has a value |
| `field` on its own | A condition: true for `has:sbom`, nonzero for counts, a non-empty value other than `false`, `0` or `no` for labels, and anything but fresh for `stale` |
| `ancestor("ref")` | The ref is anywhere in the image's ancestry |
| `and`, `or`, `not`, `( )` | Combine. `and` binds tighter than `or`. |
| `# comment` | Ignored to the end of the line |

Strings take double or single quotes; `\"` and `\\` escape inside them. Durations accept `h`, `d`, `w`, `mo` and `y` (`180d`, `12w`, `1y`). Sizes accept `B`, `KB`, `MB`, `GB`, `TB` and `KiB`, `MiB`, `GiB`, `TiB` (`512MB`, `1.5GiB`). Dates are `YYYY-MM-DD` or RFC 3339.

List fields (`tag`, `ref`, `config.ports` and the like) match when any element matches. `parent` and `root` compare against every ref that names that image, so `parent = "reg.example.com/base/go:1.22"` and `parent = "sha256:..."` both work.

## Fields

| Field | Meaning |
| --- | --- |
| `repo` | repository, including the registry host |
| `tag` | any tag pointing at the image (name only) |
| `ref` | any repo:tag or repo@digest naming the image |
| `digest` | manifest digest |
| `index` | digest of the multi-platform index holding the image |
| `created` | creation time from the image config |
| `age` | days since created |
| `pushed` | when the image was first pushed, from registries that report it (ECR) |
| `pushed_age` | days since pushed |
| `first_seen` | when strata first indexed the image |
| `size` | compressed size from the manifest |
| `layers` | number of layers |
| `platform`, `os`, `arch`, `variant` | platform, such as `linux/arm64/v8` |
| `external` | true for bases fetched from outside the indexed registries |
| `label.<key>` | any config label or manifest annotation, such as `label.team` or `label.org.opencontainers.image.source` |
| `missing:<key>` | the label is not set |
| `parent` | the parent image |
| `parent_source` | how the parent was found: `layers`, `annotation`, `label` |
| `root` | the root base |
| `generation` | number of ancestors |
| `children` | number of direct children |
| `descendants` | number of images built on this one, at any depth |
| `tags` | number of tags pointing at the image |
| `conflict` | a declared parent disagrees with the layers |
| `has:sbom`, `has:signature`, `has:attestation` | a referrer of that kind is attached |
| `stale` | verdict: `fresh`, `drifting`, `stale` or `abandoned` |
| `stale.base_drift` | generations the parent is behind its tag |
| `stale.base_drift_days` | days since the parent was replaced |
| `stale.base_tag` | the tag base drift is measured against |
| `stale.superseded` | days since a tag moved off this image |
| `stale.untagged` | no tag points at the image |
| `stale.last_pulled` | days since the last pull |
| `stale.eol_base` | the root base is past end of life |
| `stale.label_policy` | labels mark the image deprecated or expired |
| `stale.inferred` | a signal was approximated without tag history |
| `config.user` | the user the image runs as |
| `config.entrypoint`, `config.cmd` | entrypoint and cmd arguments |
| `config.ports` | exposed ports, such as `8080/tcp` |
| `config.env_keys` | environment variable names (values are never stored) |
| `config.workdir`, `config.stop_signal` | working directory and stop signal |
| `config.healthcheck` | a healthcheck is defined |

`created` and `age` come from the image config, which reproducible builds (ko, Bazel, Nix, buildpacks) often set to 1970 or another fixed date; `age` is unknown for those. On ECR, `pushed` and `pushed_age` give the real push time, and `first_seen` works on every registry.

`strata query --help` prints the same list from the binary you have installed.

## Output

| Flag | Effect |
| --- | --- |
| `--fields repo,tag,stale,label.team` | columns to return |
| `--sort -age,repo` | sort; a leading `-` sorts descending |
| `--limit 100` | cap rows |
| `--group-by label.team` | count images per value instead of listing them |
| `-o table`, `json`, `csv`, `ndjson` | format; `ndjson` streams, so a full-registry export never buffers |
| `--graph -o html` | draw the matches and their ancestry up to shared roots, see [Graphs](graphs.md) |

## Recipes

```sh
# Prod images running as root
strata query 'config.user in ["", "0", "root"] and label.env = "prod"'

# Secret-looking environment keys, by team
strata query 'config.env_keys ~ "*SECRET*" or config.env_keys ~ "*_TOKEN"' --group-by label.team

# What exposes SSH?
strata query 'config.ports = "22/tcp"'

# Images on an EOL root with no SBOM
strata query 'stale.eol_base and not has:sbom' --fields repo,tag,root

# Repos missing a required label
strata query 'missing:team' --group-by repo

# Export everything for analysis elsewhere
strata query '' -o ndjson > images.ndjson
```
