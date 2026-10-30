# Crawling large registries

`strata index` reads repos, tags, manifests and image configs. It never pulls layers, but on a registry with millions of images the request count still decides how long a crawl takes. This guide covers where those requests go and the settings that cut them.

## What a crawl costs

| Step | Requests |
| --- | --- |
| List repos | one per 1,000 repos (`_catalog`) |
| List tags | one per repo, plus one per extra page |
| Resolve each tag | one `HEAD` per tag |
| Each new image | manifest and config, plus one referrers lookup when `referrers` is on |
| Each multi-platform index | the index, then the manifest and config of every platform |

Images already in the index are skipped, so a re-crawl only lists and resolves tags. The crawl keeps one connection per repo, so the registry handshake and token exchange happen about once per repo rather than before every request.

At the end of a crawl, `strata index` prints the requests it made per new image. At about 50 ms per request and 16 workers, a first crawl of 2 million single-platform images is around 8 million requests and several hours. Raise `workers` as far as your registry's rate limits allow, and use the filters below to skip what you never query.

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
