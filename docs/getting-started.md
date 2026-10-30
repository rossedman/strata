# Install and first run

Install strata, verify the download, and index a registry. If you don't have a registry handy, the demo registry in the repository gives you a realistic one in a few seconds.

## Install

=== "Release archive"

    Download the archive for your platform from [GitHub Releases](https://github.com/rossedman/strata/releases): Linux (amd64, arm64), or macOS (amd64, arm64). Windows is not supported. Unpack it and put `strata` on your `PATH`.

=== "go install"

    strata links DuckDB through cgo, so `go install` needs a C and C++ compiler (gcc or clang) as well as Go:

    ```sh
    go install github.com/rossedman/strata@latest
    ```

Check it runs:

```sh
strata version
```

## Verify a release

Each release's `checksums.txt` is signed keylessly with Sigstore by the release workflow, and every archive carries a GitHub build provenance attestation:

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/rossedman/strata/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
gh attestation verify strata_<version>_linux_amd64.tar.gz --repo rossedman/strata
```

## Try it on the demo registry

The repository includes a demo registry seeded with about 25 images across teams, bases and an external root, including a multi-platform image, a `FROM scratch` image, an SBOM, a signature and a deprecated image. From a clone:

```sh
go run ./examples/demo-registry -addr 127.0.0.1:5000 &
strata index --registry localhost:5000 --insecure
```

```text
indexed 22 repos (0 resumed), 30 tags, 26 new images, 30 tag moves, 344 requests in 1.364s
       26 images indexed
```

Now rebuild the bases the way a week of CI would, and index again. The second crawl only fetches what changed and records each tag that moved:

```sh
curl -X POST localhost:5000/_demo/advance
strata index --registry localhost:5000 --insecure
```

```text
indexed 22 repos (0 resumed), 30 tags, 4 new images, 6 tag moves, 187 requests in 947ms
       30 images indexed
```

Ask what moved under whom:

```sh
strata stale --group-by label.team
```

```text
label.team  images  fresh  drifting  stale  abandoned  stale_pct
web         3       2      0         1      0          33.3
payments    5       2      2         0      1          20
-           12      6      6         0      0          0
platform    7       4      3         0      0          0
legacy      2       2      0         0      0          0
data        1       1      0         0      0          0
```

Then ask why one image is drifting:

```sh
strata ancestry localhost:5000/payments/api:1.8.2
```

```text
localhost:5000/payments/api:1.8.2                          sha256:d3439b79b46d drifting (base drift: 1 gen, superseded 0d ago)
└─ localhost:5000/base/go-service@sha256:11a88e115ce8      sha256:11a88e115ce8 layers
   └─ localhost:5000/base/go@sha256:af6fca2a0002           sha256:af6fca2a0002 layers
      └─ localhost:5000/mirror/debian@sha256:0b85a6d3b64d  sha256:0b85a6d3b64d layers
```

`strata explain localhost:5000/payments/api:1.8.2` gives the full picture: labels, config, every layer with the build step behind it, referrers, findings, and the staleness reasons.

## Index your registry

strata reads credentials from your Docker config, the same place `docker login` writes them:

```sh
strata index --registry reg.example.com --workers 64 --rate 500
```

- `--workers` bounds concurrent requests and `--rate` caps requests per second, so the crawl stays polite.
- If the registry has no usable `_catalog` endpoint, list repos with `--repo` or in the [config file](configuration.md).
- Add the upstream bases your images start from with `--external docker.io/library/debian:bookworm-slim`, so roots resolve to a name rather than a shared-layer placeholder.

The index lives at `$XDG_DATA_HOME/strata/strata.duckdb` (usually `~/.local/share/strata/strata.duckdb`); use `--db` to put it elsewhere. Run `strata index` on a schedule, or let [`strata serve --reindex-every 15m`](guides/serving.md) do it. Tag history starts on the first crawl, so start early: base drift is only as good as the history recorded before it.

## Next

- [Lineage](guides/lineage.md): how parents are found, and what the edge cases look like.
- [Staleness](guides/staleness.md): the signals, the verdicts and the policy file.
- [Query language](guides/query-language.md): every field and operator.
