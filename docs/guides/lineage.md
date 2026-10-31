# Lineage

strata finds every image's parent from the layer stacks the registry already stores, then confirms or contradicts it with the parents images declare. This page explains how, and what happens in the cases that don't fit.

## Chain IDs

Each image config lists `rootfs.diff_ids`, the digests of its uncompressed layers. The OCI image spec defines a *chain ID* for every prefix of that list:

```text
chain(1) = diff_id(1)
chain(n) = sha256(chain(n-1) + " " + diff_id(n))
```

Two images have the same chain ID at depth *n* exactly when their first *n* layers are identical. strata stores the chain ID of every prefix of every image in `image_prefixes`, plus each image's *top* chain ID, the chain ID of its full stack.

strata uses diff IDs rather than the layer digests in the manifest because manifest digests are of compressed blobs. The same layer recompressed with gzip or zstd gets a new digest but keeps its diff ID. That is why strata fetches every config, which costs a few kilobytes per image.

## The parent rule

An image's parent is the indexed image whose complete stack is the longest exact prefix of its own stack. In practice: take the image's prefix chain IDs from longest to shortest, skipping its own top, and the first one that is some other image's top chain ID names the parent. Recursing gives the whole ancestry back to a root.

Descendants are the reverse lookup: images with a prefix chain ID equal to a base's top chain ID. That finds everything built on a base whatever it is named, tagged or labeled.

strata computes all of this as a few set-based joins after each crawl and stores the result in the `edges` and `lineage` tables. Ancestry and descendant queries are then short recursive walks over those tables.

```sh
strata ancestry reg.example.com/payments/api:1.8.2
strata descendants reg.example.com/base/go:1.22 --depth 0
```

## Every version of a base

A tag only points at today's digest, so a plain descendant lookup misses images built on earlier builds of the same base. `--all-versions` widens the match:

| Source of base versions | Covers | Confidence |
| --- | --- | --- |
| Tag history | every digest the tag held since strata began recording | exact |
| Older images in the base repo | digests from before recording began, matched by repo and created date | inferred |
| `--versions-from file` | bases rebuilt or pushed outside the repo, one digest or ref per line | declared |

```sh
strata descendants reg.example.com/base/go:1.22 --all-versions --group-by base_version
```

The grouped form answers "how many images are still on each old version of this base?", which pairs with [base drift](staleness.md).

## Edge cases

| Case | What happens | What strata does |
| --- | --- | --- |
| Multi-stage build ending `FROM scratch` + `COPY` | No shared layers, so no inferred parent | Uses the `org.opencontainers.image.base.*` annotations or your `lineage-labels`; without them `explain` reports lineage unknown |
| Squashed or flattened image | One layer, no prefix match | Same fallback, flagged as lineage unknown |
| Several images share one top chain ID (rebuilt or retagged) | The parent is ambiguous | They are one layer stack; the earliest-created digest is the canonical parent |
| Multi-platform index | Each platform has its own stack | Lineage is computed per platform manifest; refs to the index resolve to `--platform`, default `linux/amd64` |
| Parent deleted from the registry, or never pushed | Several images share a prefix no indexed image ends at | strata records a phantom ancestor keyed by chain ID, shown as "not in registry", so the tree stays intact |
| Labels disagree with layers | Declared parent differs from the inferred one | Both edges are kept with their source; `explain` and graphs flag the conflict |

Phantom ancestors most often stand for an upstream base you have not mirrored, such as `debian:bookworm-slim`. Add it to `external` in the [config](../configuration.md) and the next crawl names it.

## Provenance

Every edge records where it came from, and graphs draw each source differently:

| Source | Confidence | Drawn as |
| --- | --- | --- |
| `layers` | exact | solid edge |
| `attestation` (BuildKit provenance) | declared | dashed edge |
| `annotation` (`org.opencontainers.image.base.digest` or `.name`) | declared | dashed edge |
| `label` (keys in `lineage-labels`) | declared | dashed edge |

When an image has a layer edge and a declared edge that point at different stacks, the layer edge wins and the image is marked `conflict`. Query them with `strata query 'conflict'`.

## Bases in other registries, and old versions

The parent rule needs the base's exact digest in the index. Two common cases break that: the base lives in a registry strata does not crawl, such as another ECR account or Docker Hub, and the image was built on a version of the base whose tag has since moved.

BuildKit (`docker buildx`) attaches build provenance to the images it pushes, by default. It lists every image the build pulled, by name and by the digest it resolved to. strata reads it while crawling (`provenance: true`, the default) and then fetches each named base that is not indexed, by digest, from whatever registry it lives in (`fetch-bases: true`, the default). Lineage then walks the image up to the exact base version it was built on, by its layers. A base from a registry that is not crawled is marked external.

A multi-stage build lists every stage's image. Only the one whose layers the image starts with becomes its parent; the others are build stages.

When a base cannot be fetched, because it was deleted or strata's credentials cannot reach its registry, strata records why and tries again a week later. The layers it left behind are then named after it, such as `base/python:3.12`, instead of "Shared layers, image not in the registry". `strata explain` and the image page list what the build pulled and where each stands:

```text
  built from, by its provenance:
    111111111111.dkr.ecr.us-east-1.amazonaws.com/base/node:20  the parent
    docker.io/library/golang:1.22  another build stage; the image does not start with its layers
    111111111111.dkr.ecr.us-east-1.amazonaws.com/base/python:3.12  not available: … MANIFEST_UNKNOWN …
```

Images indexed before strata read provenance get it too, without re-crawling: each crawl reads the provenance of the multi-platform images already indexed from that registry whose provenance was never read, about three small requests each, and only once per image. A stopped crawl resumes where it left off. Images pushed without attestations have no provenance; they are checked once and skipped after.

To reach bases in another AWS account, give that registry its own credentials; see [Scaling](scaling.md#registries-in-several-accounts). Reading provenance costs two small requests per new multi-platform index.
