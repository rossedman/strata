# Security policy

## Supported versions

strata is pre-1.0. Security fixes land on `main` and ship in the next release; only the latest release is supported.

## Reporting a vulnerability

Please report vulnerabilities privately through [GitHub private vulnerability reporting](https://github.com/rossedman/strata/security/advisories/new). Do not open a public issue.

What to expect:

- An acknowledgement within 3 business days.
- A fix or a plan within 30 days, depending on severity.
- Credit in the published advisory, unless you prefer otherwise.

strata reads registry metadata only and never pulls layer blobs, but its index maps your images, bases and their weaknesses. Reports about the index leaking through `strata serve` (auth bypass, request smuggling past the trusted-header check) are especially welcome.

## Verifying releases

Every release's `checksums.txt` is signed keylessly with [Sigstore cosign](https://docs.sigstore.dev/) by the release workflow, and every archive has a GitHub build provenance attestation. To verify a download:

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/rossedman/strata/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
gh attestation verify strata_<version>_linux_amd64.tar.gz --repo rossedman/strata
```

A signature made by any other repository or workflow fails the first command.
