# Configuration

strata reads a YAML config file, environment variables and flags. Settings resolve in this order, first match wins:

1. Command-line flags, such as `--db`.
2. Environment variables with the `STRATA_` prefix. Dots and dashes become underscores: `serve.auth-header` is `STRATA_SERVE_AUTH_HEADER`, and `--log-level` is `STRATA_LOG_LEVEL`.
3. The config file.
4. Built-in defaults.

The first config file found is used, in this order: `$XDG_CONFIG_HOME/strata/strata.yaml`, `~/.config/strata/strata.yaml`, `~/.strata.yaml`, `./strata.yaml`. Pass `--config path` to use another.

## Full example

```yaml
# Where the index lives. Default: $XDG_DATA_HOME/strata/strata.duckdb
db: /var/lib/strata/strata.duckdb

# Registries strata index crawls when --registry is not given.
registries:
  - host: reg.example.com
    workers: 64          # concurrent requests
    rate: 500            # requests per second; 0 is unlimited
  - host: registry.internal:5000
    insecure: true       # plain HTTP
    repos:               # seed the repo list when _catalog is missing or slow
      - payments/api
      - payments/worker
  - host: 123456789012.dkr.ecr.us-east-1.amazonaws.com
    # ECR hosts are listed through the ECR API with AWS credentials.
    include-repos: ["team-*/**"]   # repo globs, without the host
    exclude-repos: ["**/sandbox"]
    exclude-tags: ["pr-*", "sha-*"]
    platforms: [linux/amd64]       # skip other platforms of multi-arch images
    pushed-since: 1y               # registry APIs only: skip images pushed before this
  - host: harbor.example.com
    provider: harbor               # Harbor's API: push and pull times
  - host: us-docker.pkg.dev
    projects: [acme-prod]          # Artifact Registry: projects to list

# Upstream bases fetched so roots resolve to names.
external:
  - docker.io/library/debian:bookworm-slim
  - docker.io/library/alpine:3.20

# Fetch SBOMs, signatures and attestations (OCI referrers and cosign tags).
referrers: true

# Read BuildKit provenance, which names the base each image was built from,
# and fetch those bases by digest from any registry when they are not indexed.
provenance: true
fetch-bases: true

# Platform picked when a ref names a multi-platform index.
platform: linux/amd64

# Label keys whose value names an image's base, as repo:tag or digest.
# The OCI org.opencontainers.image.base.name/digest annotations are always read.
lineage-labels: [base-image]

# Staleness policy file; see the staleness guide.
policy: /etc/strata/policy.yaml

# Labels every image should carry. Enables missing:<key> compliance in
# summary and snapshots per team.
required: [team, org.opencontainers.image.source]

# Keys that mean the same thing, for strata labels --similar.
synonyms:
  owner: team

# Environment variables whose values are safe to store. All others are
# stored as names only.
env-allowlist: [GO_VERSION, NODE_VERSION, PYTHON_VERSION]

# Extra regular expressions scrubbed from history commands, on top of the
# built-in token, password and credential patterns.
redactions:
  - '(--token[= ])(\S+)'

serve:
  addr: 127.0.0.1:7878
  auth-header: ""        # required when addr is not loopback
  url: ""                # route CLI commands through this server

log-level: info
```

## Keys

| Key | Default | Meaning |
| --- | --- | --- |
| `db` | `$XDG_DATA_HOME/strata/strata.duckdb` | Index file |
| `registries[].host` | | Registry host, such as `reg.example.com` or `localhost:5000` |
| `registries[].repos` | catalog | Repos to crawl, without the host |
| `registries[].insecure` | `false` | Allow plain HTTP |
| `registries[].workers` | `16` | Concurrent requests |
| `registries[].rate` | `0` | Requests per second, 0 is unlimited |
| `registries[].provider` | `auto` | `auto` lists ECR, Artifact Registry and ACR hosts through their APIs; `ecr`, `harbor`, `gar` and `acr` force one; `distribution` uses the plain registry API |
| `registries[].aws-profile` | | ECR only: a named AWS profile, such as an SSO profile for the registry's account |
| `registries[].assume-role` | | ECR only: a role ARN to assume, from the profile or the default chain |
| `registries[].external-id` | | ECR only: the external ID `assume-role` requires, if any |
| `registries[].projects` | `[]` | Google Cloud projects whose Artifact Registry repositories are listed |
| `registries[].include-repos` | all | Crawl only repos matching one of these globs |
| `registries[].exclude-repos` | `[]` | Skip repos matching one of these globs |
| `registries[].include-tags` | all | Crawl only tags matching one of these globs |
| `registries[].exclude-tags` | `[]` | Skip tags matching one of these globs |
| `registries[].platforms` | all | Keep only these platforms of multi-platform images |
| `registries[].pushed-since` | | Skip images pushed longer ago than this, such as `90d` (registry APIs only) |
| `external` | `[]` | Full refs of upstream bases to fetch |
| `referrers` | `true` | Fetch referrers and cosign tags |
| `provenance` | `true` | Read BuildKit provenance, which names the base each image was built from |
| `fetch-bases` | `true` | Fetch, by digest, bases provenance names that are not indexed, from any registry |
| `platform` | `linux/amd64` | Default platform for multi-platform refs |
| `lineage-labels` | `[]` | Label keys naming a base |
| `policy` | built-in | Staleness policy file |
| `required` | `[]` | Required label keys |
| `synonyms` | `{}` | Label key synonyms |
| `env-allowlist` | `[]` | Environment variables whose values are stored |
| `redactions` | `[]` | Extra history redaction patterns |
| `serve.addr` | `127.0.0.1:7878` | Listen address for `strata serve` |
| `serve.auth-header` | | Trusted header set by your auth proxy |
| `serve.url` | | Server CLI commands should query instead of the file |
| `log-level` | `info` | `debug`, `info`, `warn` or `error` |

See [Scaling to large registries](guides/scaling.md) for sizing, the filters, the ECR API and background indexing.

Accessible output for screen readers: set `STRATA_ACCESSIBLE=1` to replace the spinner with plain progress lines. Colors follow `NO_COLOR` and switch off when output is not a terminal.
