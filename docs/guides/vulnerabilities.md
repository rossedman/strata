# Vulnerabilities

When a base image has a CVE, the question is what is built on it, who owns
that, and what is actually running. strata answers it from the registry's
own vulnerability scans: it doesn't scan anything itself.

That means ECR, which scans images with basic scanning or with Amazon
Inspector (enhanced scanning), Harbor, with its configured scanner (Trivy by
default), and Google Artifact Registry, with Artifact Analysis. Azure
doesn't expose Defender's results through the registry, so ACR images have
no scans.

## What is recorded

| Data | Where it comes from | Cost |
| --- | --- | --- |
| Counts by severity, scan time | the `DescribeImages` listing, under enhanced scanning | free: it comes with the listing |
| Counts and every finding: id, package, version, fixed version | `DescribeImageScanFindings`, with `scan-findings: true` | one call per image whose scan changed |
| Harbor: counts by severity, scan time | the artifacts listing | free |
| Harbor: every finding | the artifact's vulnerability report, with `scan-findings` | two calls per image whose scan changed |
| Artifact Registry: counts and every finding | Artifact Analysis occurrences, with `scan-findings` | one call per image, again weekly |

Basic scanning leaves results out of the listing, so with it, counts and
findings both need `scan-findings`:

```yaml
registries:
  - host: 123456789012.dkr.ecr.us-east-1.amazonaws.com
    scan-findings: true
```

or `strata index --scan-findings`. strata asks for an image's findings again
when the listing reports a new scan or new counts, and, when the listing
reports no scan at all, once a week. A first crawl makes one call per
image; at 2 million images, start with `include-repos` on the repos that
matter. Findings need `ecr:DescribeImageScanFindings`.

## Query

Every image row has `vuln.critical`, `vuln.high`, `vuln.medium`, `vuln.low`,
`vuln.total`, `vuln.fixable`, `vuln.scanned` and `vuln.scanned_at`, and
filters can match `vuln.id` and `vuln.package`:

```sh
# Critical vulnerabilities in images pulled in the last 30 days, by team
strata query 'vuln.critical > 0 and stale.last_pulled < 30d' --group-by label.team

# Which images still have xz's backdoor, and who owns them
strata query 'vuln.id = "CVE-2024-3094"' --fields repo,tag,label.team

# Fixable criticals on bases other images are built on
strata query 'vuln.fixable > 0 and descendants > 0' --sort -descendants
```

strata doesn't know what is deployed. A recent pull is the closest signal
it has, and on ECR `stale.last_pulled` comes with the listing.

## Which bases matter most

```sh
strata vulns bases
strata vulns bases --severity high --in-use 7d
```

Ranks base images with findings by their critical count times the images
built on them that are in use (pulled within `--in-use`, default 30 days),
then times all images built on them. Columns:

| Column | Meaning |
| --- | --- |
| `critical`, `high`, `fixable` | the base's own findings |
| `images` | images built on it, at any depth |
| `in_use` | of those, pulled within the window |
| `vulnerable` | of those, images whose own scan has findings of that severity too |
| `groups` | distinct teams among them (`--group-by` picks another field) |

## Blast radius

```sh
strata vulns impact reg.io/base/debian:bookworm
strata vulns impact CVE-2024-3094 --where 'stale.last_pulled < 30d'
strata vulns impact CVE-2024-3094 -o csv > cve-2024-3094.csv
```

For a base, the blast radius is every image built on it. For a vulnerability
id, it is every image whose scan found it, plus unscanned images built on one
of those. An image scanned without the vulnerability stops the walk, because
it was patched or rebuilt. **Enters through** lists the images where the
vulnerability first appears in each lineage: an image that has it while its
parent doesn't.

The table totals images per team, with how many are in use and in how many
repos. `--images` lists every image, with `evidence`:

- `scan`: its own scan found the vulnerability
- `built on`: built on the base asked about
- `inherited`: not scanned, but built on an image that has it

`-o csv` and `-o ndjson` export those rows for a ticket or a spreadsheet, and
`--group-by repo` or any other field replaces `label.team`.

## In the UI and API

The **Vulnerabilities** view in `strata serve` ranks the bases. Click one, or
enter a CVE, to see the blast radius by team, with a **Download CSV** link.

The same reports are `GET /v1/vulns/bases` and `GET /v1/vulns/impact?cve=...`
(`format=csv` exports), and the `vuln_bases` and `vuln_impact` MCP tools, so
an agent can answer "which teams run images with CVE-2024-3094?"
