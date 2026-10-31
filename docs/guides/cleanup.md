# Cleanup

Registries fill up with images nobody uses. `strata cleanup` lists the ones
that can go, and what deleting them really frees once layers shared with
the images you keep are taken out. strata never deletes anything itself.

```sh
strata cleanup
strata cleanup -o csv > candidates.csv
```

## Candidates

By default the candidates are images the [staleness policy](staleness.md)
calls **abandoned**: untagged, nothing built on them, and not pulled within
`abandoned_pull_days` (90 days by default). On ECR an image never pulled
counts as unpulled once its push is older than that.

`--where` picks candidates with the [query language](query-language.md)
instead:

```sh
strata cleanup --where 'stale = "abandoned" and repo ~ "*/sandbox/*"'
strata cleanup --where 'tags = 0 and pushed_age > 180d and descendants = 0'
```

Whatever the filter, two rules keep a deletion from breaking anything:

- A multi-platform index is a candidate only when all its platform images
  are. Deleting it deletes them all.
- An image a kept image is built on is held, with its whole index. Held
  images are counted in the summary line.

## Reclaimable storage

Manifest sizes overstate what a deletion frees, because images share
layers. strata counts each layer once, and only when no kept image uses it:

| Number | Meaning |
| --- | --- |
| `size` | the candidates' sizes as their manifests state them, a shared layer counted for each image |
| `reclaimable_bytes` | the layers deleting every candidate frees, each counted once |
| `exclusive_bytes` | per candidate: what deleting it alone frees, its layers no other image uses |
| per repo `reclaimable_bytes` | what deleting the repo's candidates frees |

Two candidates sharing a big layer each show a small `exclusive_bytes`, but
deleting both frees the layer, and the total counts it.

`--scope registry`, the default, keeps a layer when any kept image in the
registry uses it, which suits registries that store each blob once. ECR
stores layers per repository, so `--scope repo`, which keeps a layer only
when a kept image in the same repo uses it, is closer to what an ECR bill
shows. Image configs, which are small, are not counted. Real storage also
depends on when the registry collects garbage.

## ECR lifecycle policies

```sh
strata cleanup lifecycle
strata cleanup lifecycle -o script > apply-lifecycle.sh
```

Generates, for each repo, an ECR lifecycle policy that expires untagged
images pushed more than `abandoned_pull_days` ago (or `--days`), so ECR does
the cleanup on its own schedule:

```json
{"rules":[{"rulePriority":1,"description":"strata: expire untagged images pushed more than 90 days ago",
  "selection":{"tagStatus":"untagged","countType":"sinceImagePushed","countUnit":"days","countNumber":90},
  "action":{"type":"expire"}}]}
```

ECR's expire rules can see push age, but not pulls or what is built on an
image. So strata checks each rule against the index before writing it. A repo
where the rule would expire an image strata keeps gets no policy, and the
table says why:

- the image was pulled recently
- other images are built on it (a base referenced by digest)
- it is a platform image of a tagged index, which ECR sees as untagged

`--include-conflicts` writes the rule anyway. `would_expire` and `agree` show
how many images the rule expires today and how many of those strata lists
too.

`-o script` prints `aws ecr put-lifecycle-policy` commands to review and run;
`-o json` gives the policy documents. Each policy replaces the repository's
existing one. Preview a policy with `aws ecr start-lifecycle-policy-preview`
before applying it.

## In the UI and API

The **Cleanup** view in `strata serve` shows reclaimable storage, the repos
that free the most, the candidates with a **Download CSV** link, and which
repos get a lifecycle policy. The reports are `GET /v1/cleanup` and
`GET /v1/cleanup/lifecycle`, and the `cleanup` MCP tool.
