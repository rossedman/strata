# Staleness

Every image gets a verdict, fresh, drifting, stale or abandoned, and every verdict lists the reasons that produced it. There is no opaque score.

The strongest signal is base drift: the image's parent is no longer what that parent's tag points to. Age alone is a weak proxy. A two-year-old image on the current base is fine; a two-week-old image on a superseded base is not.

## Signals

| Signal | Meaning | Derived from | Query field |
| --- | --- | --- | --- |
| Base drift | The parent was replaced under its tag, counted in generations behind | Lineage and tag history | `stale.base_drift`, `stale.base_drift_days`, `stale.base_tag` |
| Superseded | A tag that pointed at this image now points at a newer one | Tag history | `stale.superseded` |
| Untagged | No tag points at the image | Tags | `stale.untagged` |
| Age | Days since `created` in the config (unknown for reproducible builds that zero it) | Image config | `age` |
| Last pulled | No pulls in N days | Pull data, where the index has it | `stale.last_pulled` |
| EOL base | The root base is past end of life | `eol_roots` in the policy | `stale.eol_base` |
| Label policy | Labels say so, such as `deprecated=true` or a past `expires` date | Labels and annotations | `stale.label_policy` |

When an image's parent was never tagged during recording, strata approximates drift: if the parent's repo holds a newer image created after the parent, the parent was likely superseded. Those signals are marked `stale.inferred`.

## Verdicts

| Verdict | When |
| --- | --- |
| **abandoned** | untagged, no descendants, and no pulls within `abandoned_pull_days` (with no pull data at all, untagged leaves count as unpulled) |
| **stale** | base drift of `stale_generations` or more, superseded more than `superseded_days` ago, an EOL root, or a label policy match |
| **drifting** | base drift of at least 1 generation, or superseded within `superseded_days` |
| **fresh** | none of the above |

The reasons read like this:

```text
base reg.example.com/base/go:1.22 moved 3 times since this image's parent (last move 41 days ago)
superseded under reg.example.com/payments/api:latest 12 days ago
root base docker.io/library/debian:buster-slim is past end of life
```

## The policy file

Point `policy` in the [config](../configuration.md) at a YAML file. Every key is optional; these are the defaults, which are starting points to tune rather than recommendations:

```yaml
thresholds:
  # Generations of base drift that make an image stale.
  stale_generations: 2
  # Superseded longer than this is stale; shorter is drifting.
  superseded_days: 30
  # Untagged leaves unpulled this long are abandoned.
  abandoned_pull_days: 90

# A true, yes or 1 value marks an image stale.
deprecated_labels: [deprecated]
# A past date (YYYY-MM-DD or RFC 3339) marks an image stale.
expires_labels: [expires]

# Root refs or digests past end of life. * matches within a path segment.
eol_roots:
  - docker.io/library/debian:buster*
  - docker.io/library/alpine:3.16*

# Treat untagged leaves as unpulled when the index has no pull data.
assume_unpulled: true

# Applied in order to matching images.
overrides:
  - repo: "reg.example.com/sandbox/*"
    thresholds: { superseded_days: 7 }
  - labels: { env: prod }
    thresholds: { stale_generations: 1 }
```

After changing the policy, recompute verdicts without crawling:

```sh
strata refresh
```

## Tag history starts on day one

Registries only expose where a tag points now. strata records every tag move from its first crawl onward, and base drift is only as good as the history recorded before it. Start indexing early, even before you rely on the verdicts.

Tag history is also the one part of the index that cannot be rebuilt from the registry, so keep a copy:

```sh
strata history export tag-history.parquet
strata history import tag-history.parquet   # into a fresh index, then refresh
```

## Reports

```sh
strata stale                         # the mix for the whole index
strata stale --group-by label.team   # per team
strata stale --group-by root         # per root base
strata explain <ref>                 # the reasons for one image
strata query 'stale in ["stale", "abandoned"] and label.env = "prod"'
```
