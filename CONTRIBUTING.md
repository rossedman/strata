# Contributing to strata

Thanks for helping. This page covers setup, the checks every change must pass, and how changes get merged.

## Setup

strata links DuckDB through cgo, so you need Go (the version in `go.mod`) and a C/C++ compiler (gcc or clang).

```sh
git clone https://github.com/rossedman/strata
cd strata
make build          # bin/strata
make test           # race tests
```

To try it against a realistic registry without credentials, run the demo registry and index it:

```sh
go run ./examples/demo-registry -addr 127.0.0.1:5000 &
bin/strata index --registry 127.0.0.1:5000 --insecure
curl -X POST 127.0.0.1:5000/_demo/advance   # rebuild bases, move tags
bin/strata index --registry 127.0.0.1:5000 --insecure
bin/strata stale --group-by label.team
```

## Checks

`make check` runs everything CI runs. The individual targets:

| Target | What it checks |
| --- | --- |
| `make lint` | golangci-lint, the OpenAPI spec (vacuum), one cobra command per file, doc comments on every function |
| `make test` | `go test -race ./...` |
| `make cover` | coverage of hand-written code is at least 90% (generated files, `examples/` and test helpers excluded) |
| `make generate-check` | API code generated from `api/openapi.yaml` is current |
| `make bench` | benchmarks, saved to `bench.txt` for `benchstat` |
| `make fuzz` | every fuzz target, 30s each |
| `make docs-build` | the docs site builds with `--strict` |

The HTTP API is spec-first: change `api/openapi.yaml`, run `make generate`, then implement. Never edit `*.gen.go` files.

## Quality bars

- Every function and method has a doc comment, checked by `go run ./tools/doccheck ./...`.
- Each file in `cmd/` defines exactly one cobra command, named after the file.
- Coverage stays at or above 90% on hand-written code.
- Every critical path below has a benchmark, and every one that takes external input has a fuzz target.

## Critical paths

These parse input, transform data or sit in hot loops. Changes to them need a benchmark run before and after (`make bench`, then `benchstat`), and the input-handling ones keep a fuzz target.

| Path | Why | Benchmark | Fuzz |
| --- | --- | --- | --- |
| `internal/chain.IDs` | computes chain IDs for every layer of every image | `BenchmarkIDs` | `FuzzIDs` |
| `internal/query.Compile` | parses user filters and builds SQL | `BenchmarkCompile` | `FuzzCompile` |
| `internal/crawl.Redactor.Redact` | scrubs secrets from every history command | `BenchmarkRedact` | `FuzzRedact` |
| `internal/graph.Graph.Budget` | collapses descendant trees of popular bases | `BenchmarkBudget` | `FuzzBudget` |
| `internal/stale.Evaluator.Evaluate` | runs once per image on every refresh | `BenchmarkEvaluate` | |
| `internal/store.Store.Write` | bulk-loads every crawl batch | `BenchmarkWrite` | |
| `internal/service.Service.Resolve` | resolves every ref a command or API call names | `BenchmarkResolve` | |

## Commits and pull requests

Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages and PR titles:

```text
feat(query): add a has:provenance field
fix(crawl): skip attestation manifests in indexes
docs: explain base drift before tag history begins
```

Pull requests are squash-merged. Each needs green checks and a review from a code owner (see `.github/CODEOWNERS`). Keep a PR to one change; say what it does, why, and how you tested it.

## Questions

Open a [discussion or issue](https://github.com/rossedman/strata/issues). For anything security-related, follow [SECURITY.md](https://github.com/rossedman/strata/blob/main/SECURITY.md) instead.
