# Roadmap

strata v0.1 implements the core of the design: indexing, lineage, staleness, the query language, footprint reports, graphs, the HTTP API and MCP. This page lists what is in, and what comes next.

## In v0.1

- Resumable crawl over the OCI Distribution API with rate limits, digest dedup, multi-platform indexes, OCI referrers and cosign tags
- Tag history from the first crawl, with Parquet export and import
- Lineage from chain IDs, phantom ancestors, declared parents from OCI annotations and labels, conflict detection
- Staleness verdicts with reasons and a policy file with overrides
- The filter language, compiled to SQL
- `summary`, `labels`, `layers`, `bases similar`, daily snapshots, `diff`, `trend`, `map`
- Graph output as tree, JSON, Mermaid, DOT, SVG and self-contained HTML, under a node budget
- `strata serve`: an OpenAPI-generated JSON API, MCP over HTTP, scheduled re-crawls, trusted-header auth, CLI routing
- `strata mcp` over stdio
- A web UI served by `strata serve` and `strata ui`: an overview, a lineage outline that loads one level at a time, search with facet counts, and an image page, backed by per-subtree counts and a materialized image table so it stays fast at millions of images
- Listing through the ECR, Harbor, Google Artifact Registry and Azure Container Registry APIs, with push times, and pull times on ECR and Harbor

## Next

- **Webhooks and `strata watch`**: normalize Harbor, ECR (via EventBridge), distribution notifications and Artifactory events into push, delete and tag-move events, so the index stays current within a minute.
- **Incremental staleness**: when a tag moves, re-evaluate only that image's descendants instead of the whole index.
- **Pull data**: registry adapters for pull timestamps (Artifactory) to drive the last-pulled and leaf-with-no-use signals.
- **SBOM-based EOL detection**: read the OS release from SBOM referrers instead of relying only on a curated roots list.

## Later

- Serving the index over DuckDB's Quack protocol, so teams can share one file without the HTTP layer.
- A Postgres backend for large shared deployments with many concurrent writers.
- Built-in OIDC for `strata serve`, beside trusted-header auth.
- A golden-bases allowlist, so `explain` can flag images built on unapproved roots.
