# Verify a release

[English](release-verification.md) | [简体中文](release-verification.zh-CN.md)

Download the binary for your host, `SHA256SUMS`, and the matching `maniud_X.Y.Z.sigstore.json` bundle from [GitHub Releases](https://github.com/IceCodeNew/maniud/releases).

| Artifact suffix | Release verification |
| --- | --- |
| `linux_amd64` | Built and tested on Linux amd64. |
| `linux_arm64` | Built and smoke-tested on Linux arm64. |
| `darwin_arm64` | Built and tested on macOS arm64. |
| `darwin_amd64` | Cross-built without a native Intel Mac test. |

All release binaries use `CGO_ENABLED=0`.

## Check the SHA-256 digest

Set the downloaded filename and compare it with its exact checksum entry:

```sh
artifact=maniud_X.Y.Z_linux_amd64
expected="$(awk -v file="$artifact" '$2 == file { print $1 }' SHA256SUMS)"
actual="$(shasum -a 256 "$artifact" | awk '{ print $1 }')"
test -n "$expected" && test "$actual" = "$expected"
```

Stop if the entry is missing or the digest differs.

## Verify the Sigstore attestations

Verify the selected binary against the repository, Release workflow, master branch, and bundle:

```sh
gh attestation verify "$artifact" \
  --repo IceCodeNew/maniud \
  --signer-workflow IceCodeNew/maniud/.github/workflows/release.yml \
  --source-ref refs/heads/master \
  --deny-self-hosted-runners \
  --bundle maniud_X.Y.Z.sigstore.json
```

Verify `SHA256SUMS` before trusting any checksum entry:

```sh
gh attestation verify SHA256SUMS \
  --repo IceCodeNew/maniud \
  --signer-workflow IceCodeNew/maniud/.github/workflows/release.yml \
  --source-ref refs/heads/master \
  --deny-self-hosted-runners \
  --bundle maniud_X.Y.Z.sigstore.json
```

Reject the download when the repository, workflow, source branch, subject digest, or checksum does not match.
After verification, install the binary and confirm that `maniud --version` reports the expected release.
