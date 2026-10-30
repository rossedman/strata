# Exploring the footprint

Five commands turn the index into an understanding of the whole registry: what it holds, what metadata it carries, where the bytes go, and how it changes. All of them read manifests and configs only.

## Summary

```sh
strata summary
strata summary --group-by label.team
strata summary --group-by root -o json
```

| Metric | Meaning |
| --- | --- |
| repos, tags, manifests | raw counts |
| unique layer stacks | distinct top chain IDs, which shows how much is just retagging |
| untagged | share of digests no tag points to |
| platforms | count per OS and architecture |
| apparent bytes | sum of every image's compressed layer sizes, as manifests state them |
| deduplicated bytes | each layer blob counted once, close to real storage |
| exclusive bytes | layers only this group uses: what removing it would free |
| shared-cost bytes | each shared layer's size split equally among the groups using it: who uses most |
| staleness mix | fresh, drifting, stale and abandoned counts |

Grouped by `label.team`, this is a storage showback report. Manifest sizes are compressed sizes; real disk use also depends on the registry's garbage collection.

## Labels

With an inherited registry, nobody knows which labels exist or can be trusted.

```sh
strata labels            # every key: coverage, distinct values, top values
strata labels team       # one key: value counts, repos lacking it, first and last seen
strata labels --similar  # near-duplicate keys: team, Team, team_name, and configured synonyms
```

Distinct-value counts separate enums like `env` from free text like `commit`. Near-duplicates are reported so they can be unified at the source; strata never rewrites anything. Keys listed in `required` in the config show up in `strata labels` even when no image has them, and `summary` reports their coverage.

## Config metadata

The configs strata fetches for lineage also say how each image runs, and those fields are queryable under `config.*`: user, entrypoint, cmd, exposed ports, environment keys, workdir, stop signal and healthcheck. Environment values are never stored, only keys, except for the ones you allowlist in `env-allowlist`. History commands pass through redaction patterns before they are stored, since build arguments can carry secrets. A secret-looking key is reported as a finding by `strata explain` without the value ever being held.

## Layers

```sh
strata layers top --by shares        # the real foundations of the registry
strata layers top --by size          # biggest layers overall
strata layers bloat --min-size 500MB # big layers only one image uses, with the command behind them
strata layers bloat --group-by repo  # big layers only one repo uses
strata bases similar                 # near-duplicate bases worth consolidating
```

Each non-empty history entry maps to one layer, so every layer traces back to the build step that created it. `bases similar` groups bases whose stacks match except for their last one or two layers, with an estimate of the deduplicated bytes saved by standardizing each group on one base.

## Change over time

Every index run writes a daily snapshot of the summary metrics, overall and per repo, per root and per required label. Like tag history, snapshots begin on the first run.

```sh
strata diff --since 7d                # new and removed repos, new roots, growth, staleness shifts, label coverage
strata diff --since 30d --dark-days 90 # plus repos with no push in 90 days that still hold tags
strata trend dedup_bytes -o html > storage.html
strata trend stale --group-by root -o csv
```

`strata trend` draws any snapshot metric as a self-contained HTML line chart, or writes the rows for analysis elsewhere.
