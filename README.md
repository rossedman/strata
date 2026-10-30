<p align="center"><img src="docs/assets/banner.png" alt="strata: every image's lineage and staleness, from registry metadata alone"></p>

<p align="center">
  <a href="https://github.com/rossedman/strata/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/rossedman/strata/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/rossedman/strata/actions/workflows/ci.yml"><img alt="Coverage gate: 90%" src="https://img.shields.io/badge/coverage%20gate-90%25-5fd39a"></a>
  <a href="https://goreportcard.com/report/github.com/rossedman/strata"><img alt="Go Report Card" src="https://goreportcard.com/badge/github.com/rossedman/strata"></a>
  <a href="https://github.com/rossedman/strata/releases"><img alt="Release" src="https://img.shields.io/github/v/release/rossedman/strata?include_prereleases&color=a99bff"></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-a99bff"></a>
  <a href="https://rossedman.github.io/strata/"><img alt="Docs" src="https://img.shields.io/badge/docs-rossedman.github.io%2Fstrata-a99bff"></a>
</p>

strata indexes a container registry into one local DuckDB file and answers three questions about any image:

- **What is it?** Labels, annotations, config, layers with the build step behind each, size and referrers.
- **Where did it come from?** Its ancestry back to the root base, with parents found from exact layer-prefix matches and confirmed by OCI base annotations or labels.
- **Is it stale?** An explainable verdict, fresh, drifting, stale or abandoned, built from base drift, supersession, tag status and policy, with the reasons spelled out.

It needs registry read access and nothing else: no source repos, no CI, no database server. It reads manifests and configs and never pulls a layer blob.

## Install

```sh
go install github.com/rossedman/strata@latest   # needs a C compiler: DuckDB links through cgo
```

Or download a signed archive for Linux or macOS from [Releases](https://github.com/rossedman/strata/releases). [Verifying releases](SECURITY.md#verifying-releases) shows how to check the signature and provenance.

## Quick start

```sh
strata index --registry reg.example.com          # credentials come from your Docker config
strata stale --group-by label.team               # where are the stale images?
strata explain reg.example.com/payments/api:1.8.2
strata descendants reg.example.com/base/go:1.22 --all-versions --depth 0
strata query 'label.team = "payments" and age > 180d and not has:sbom'
strata ui                                         # the web UI on 127.0.0.1:7878
strata serve                                      # web UI, JSON API and MCP for a team
```

No registry handy? `go run ./examples/demo-registry` starts one seeded with realistic images; the [getting-started guide](https://rossedman.github.io/strata/getting-started/) walks through it.

## Configuration

Settings resolve flag first, then `STRATA_*` environment variables, then `strata.yaml`, then defaults. The config file is found at `$XDG_CONFIG_HOME/strata/strata.yaml`, `~/.config/strata/strata.yaml`, `~/.strata.yaml` or `./strata.yaml`. See [Configuration](https://rossedman.github.io/strata/configuration/) for every key.

## Documentation

Everything else is at **[rossedman.github.io/strata](https://rossedman.github.io/strata/)**: how [lineage](https://rossedman.github.io/strata/guides/lineage/) and [staleness](https://rossedman.github.io/strata/guides/staleness/) work, the [query language](https://rossedman.github.io/strata/guides/query-language/), [graphs](https://rossedman.github.io/strata/guides/graphs/), [serving and MCP](https://rossedman.github.io/strata/guides/serving/), and the generated command and API reference.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). `make check` runs everything CI runs. Security issues go through [SECURITY.md](SECURITY.md).

strata is licensed under [Apache-2.0](LICENSE).
