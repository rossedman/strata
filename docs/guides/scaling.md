# Scaling to large registries

strata indexes registries with millions of images. A first crawl of that size takes hours, so this guide covers what the time goes on, how to size the host, which settings cut the work, and how to keep the UI usable while the crawl runs.

## The short version

For an ECR registry with around 2 million images:

```yaml
# strata.yaml
db: /var/lib/strata/strata.duckdb
registries:
  - host: 123456789012.dkr.ecr.us-east-1.amazonaws.com
    workers: 64
    exclude-tags: ["pr-*", "sha-*"]
    platforms: [linux/amd64]
```

```sh
strata serve --reindex-every 6h --refresh-every 30m
```

The server crawls at startup and every 6 hours after that. The UI is usable from the first minute and fills in as the crawl goes. Run it on a host with 4 or more vCPUs, 16 GB of memory and an SSD with 40 GB free.

## Where the time goes

A crawl reads repos, tags, manifests and image configs. It never pulls layers.

| Step | Requests |
| --- | --- |
| List repos | one per 1,000 repos (`_catalog`), or none with a seeded `repos` list |
| List tags | one per repo, plus one per extra page |
| Resolve each tag | one `HEAD` per tag |
| Each new image | manifest and config, plus one referrers lookup when `referrers` is on |
| Each multi-platform index | the index, then the manifest and config of every platform |

Images already in the index are skipped, so a re-crawl only lists and resolves tags. The crawl keeps one connection per repo, so the registry handshake and token exchange happen about once per repo rather than before every request.

After the crawl, strata brings lineage, staleness and the materialized views up to date, called a refresh. The first refresh builds everything. Later ones are incremental:

- Lineage is skipped when no image, tag or index entry changed, and otherwise only rows that differ are written.
- Staleness depends on the date, so every image is evaluated, but only verdicts that changed are written.
- The table queries and the UI read is rebuilt only for images that changed, or whose parent's or root's tags moved.
- Today's snapshot is skipped when no image changed.

`GET /v1/status` reports what the last refresh rewrote, under `last_refresh`.

These numbers come from the benchmarks in the repository, run on a 4-vCPU machine. Your registry's latency and limits will move them, so use them as a starting point:

| What | Measured |
| --- | --- |
| First crawl, synthetic registry, 2 ms per request | about 400 images/s with 4 workers, 850 with 16 to 64 |
| Requests per new image | 3.3 (2.4 through the ECR API) |
| Re-crawl | 1.3 requests per tag; through the ECR API, about one call per 1,000 images |
| First (full) refresh | 28 s at 200,000 images |
| Refresh after a re-crawl with no changes | 5 s at 200,000 images |
| Refresh after 100 new images and 50 tag moves | 14 s at 200,000 images |
| Index size | about 4.5 KB per image for 3-layer images; deeper images take more |
| Peak memory | 2.9 GB refreshing 200,000 images |

Extrapolated to 2 million images, these give:

- about 6.5 million requests for a first crawl;
- a first refresh of 5 to 10 minutes, and routine refreshes of 1 to 3 minutes;
- an index of 10 to 20 GB.

With 50 ms of registry latency and 64 workers, the requests alone take about an hour and a half. Expect a first crawl of 1 to 3 hours, depending on how many requests per second your registry allows.

## Sizing the host

- **CPU:** 4 vCPUs or more. Crawl workers, index writes and refresh all use several cores.
- **Memory:** 16 GB for millions of images, 8 GB up to a few hundred thousand. DuckDB caps itself at 80% of the machine's memory by default.
- **Disk:** a local SSD with room for twice the index: the expected size, plus the same again for the write-ahead log and temporary space during a refresh. Avoid network file systems; DuckDB needs a local file it can lock.

## Workers and rate limits

`workers` sets how many requests run at once for a registry, and `rate` caps requests per second. More workers help until the registry pushes back.

```yaml
registries:
  - host: reg.example.com
    workers: 64
    rate: 500
```

Start with 32 to 64 workers. Registries answer throttled requests with HTTP 429, and the registry client retries those with backoff; if the log shows many retries, or the registry team asks, set `rate` below what they allow. ECR applies per-account API quotas, which you can see and raise in AWS Service Quotas.

## Filters

Each filter runs before the requests it saves.

```yaml
registries:
  - host: reg.example.com
    include-repos: ["team-*/**"]
    exclude-repos: ["**/sandbox", "scratch/*"]
    exclude-tags: ["pr-*", "sha-*", "*-dev"]
    platforms: [linux/amd64]
```

| Setting | Skips | Saves |
| --- | --- | --- |
| `include-repos`, `exclude-repos` | whole repos | the tag listing and everything under it |
| `include-tags`, `exclude-tags` | tags | the `HEAD` and, for new images, the manifest and config |
| `platforms` | other platforms in a multi-platform index | that platform's manifest, config and referrers |
| `pushed-since` | images pushed before the window (ECR only) | the manifest and config |

The same settings are flags on `strata index`: `--include-repo`, `--exclude-repo`, `--include-tag`, `--exclude-tag`, `--platforms` and `--pushed-since`, each repeatable where it takes a list.

Globs match repo names without the host. `*` and `?` stay within one path segment, `**` crosses segments (`team/**` matches `team/api` and `team/a/b`), and `[...]` is a character class (`[!v]*` negates it). An include list keeps only what matches; an exclude list then drops from that.

Repos and tags a filter skips are left as the index last saw them. strata only deletes tags the registry no longer lists, so narrowing a filter never records a deletion in tag history.

`platforms` only applies inside multi-platform indexes. A single-platform image's platform is in its config, so by the time strata knows it, the requests are already made; those images are kept. A platform without a variant, such as `linux/arm64`, matches every variant.

Lineage follows whatever is indexed. If a filter skips a base image, its children still group under the shared layers as a branch point, but the base won't have a name. List bases you filter out under `external` to keep their names.

## Amazon ECR

strata lists ECR registries (`<account>.dkr.ecr.<region>.amazonaws.com`) through the ECR API instead of the registry protocol:

- One `DescribeImages` page returns up to 1,000 manifests with their tags, so tags need no `HEAD` requests. A re-crawl of 2 million images is about 2,000 API calls.
- The listing includes when each image was pushed and last pulled. Push times are queryable as `pushed` and `pushed_age`, and appear as a **Pushed** facet in the web UI.
- The last-pull time feeds the staleness engine's pull signal (`stale.last_pulled`), with no pull data to import. ECR updates it about once a day.
- Artifacts attached to an image, such as SBOMs and signatures, come with the listing, so there are no referrers requests.
- `pushed-since` skips images pushed before the window without fetching them.

```yaml
registries:
  - host: 123456789012.dkr.ecr.us-east-1.amazonaws.com
    workers: 64
    pushed-since: 1y
    exclude-tags: ["pr-*"]
```

`pushed-since` only skips images strata hasn't indexed yet. An image already in the index stays, and its tags keep updating, after it ages out of the window.

Credentials come from the standard AWS chain: environment variables, `AWS_PROFILE` and SSO profiles, EKS web identity and instance roles. No Docker credential helper is needed. The region comes from the host. The identity needs:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*" },
    {
      "Effect": "Allow",
      "Action": ["ecr:DescribeRepositories", "ecr:DescribeImages", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"],
      "Resource": "arn:aws:ecr:us-east-1:123456789012:repository/*"
    }
  ]
}
```

Set `provider: distribution` to crawl an ECR host through the plain registry protocol instead, for example when the identity can pull but not describe. Push and pull times and `pushed-since` aren't available then.

## Index in the background

`strata serve --reindex-every` crawls the configured registries at startup, then on that schedule. The web UI, the API and MCP all work while it runs:

- A banner at the top of the UI shows the registry, the repos done out of the total and the new images so far. It offers **Show latest** when the views have refreshed since the page loaded.
- Every view answers from what is indexed so far. Crawled rows are written every few seconds.
- Lineage, staleness and the views refresh every `--refresh-every` (default 10 minutes) while the crawl finds new images, and once more when it finishes. A refresh during a first crawl writes most of the index, so each one waits at least as long as the last took before the next starts. At 2 million images, `--refresh-every 30m` keeps refreshes from slowing the crawl. `--refresh-every 0` refreshes only at the end.
- `GET /v1/status` returns the same progress as JSON, for monitoring.

Until a base image's repo is crawled, images built on it are grouped under "Shared layers, image not in the registry" in the UI. They move under the base's name at the next refresh after its repo is indexed. To have bases named from the start, crawl them first:

```sh
strata index --include-repo 'base/**'
strata serve --reindex-every 6h
```

A crawl is resumable. If the server stops part way through, the next crawl skips the repos the last one finished and picks up the rest. Each repo is checkpointed in the same transaction as its data.

While the server runs, it holds the index file, and read commands such as `strata query` and `strata stale` route through it. See [Serving, API and MCP](serving.md#one-owner-of-the-index).

## Re-crawls

A re-crawl through the distribution API costs one `HEAD` per tag: about 2 million requests for 2 million tags. Through the ECR API, the same re-crawl is about 2,000 calls. Set `--reindex-every` from that: hourly is cheap on ECR, while 6 to 12 hours suits a large registry crawled tag by tag.

## Watching performance

- `strata index` prints the requests it made per new image and what the filters skipped. The server logs the same at the end of each crawl, in the `crawl finished` line.
- Every pull request runs the crawl, refresh and write benchmarks against the base branch and posts a `benchstat` comparison to the job summary. Timings are reported but don't fail the build, because shared CI runners are noisy.
- `TestRequestBudget` pins the exact number of requests of each kind a crawl makes, so a change that adds requests fails the tests.
- Locally, `make bench-compare` runs the same comparison against `main`, or against another branch with `BASE=<branch>`.
