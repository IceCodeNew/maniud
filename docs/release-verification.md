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

## Install a verified release with GitHub CLI

Install [GitHub CLI](https://cli.github.com/) first. This block selects the current stable release and your host architecture, verifies both downloaded files against the release bundle, checks the one matching checksum entry, and installs the binary under `$HOME/.local/bin`.

```sh
set -eu

repo='IceCodeNew/maniud'
tag="$(gh release view --repo "$repo" --json tagName --jq .tagName)"
version="${tag#v}"

case "$(uname -s):$(uname -m)" in
  Darwin:arm64)  platform='darwin_arm64' ;;
  Darwin:x86_64) platform='darwin_amd64' ;;
  Linux:aarch64 | Linux:arm64) platform='linux_arm64' ;;
  Linux:x86_64) platform='linux_amd64' ;;
  *) printf 'unsupported host: %s %s\n' "$(uname -s)" "$(uname -m)" >&2; exit 1 ;;
esac

artifact="maniud_${version}_${platform}"
bundle="maniud_${version}.sigstore.json"
workdir="$(mktemp -d "${TMPDIR:-/tmp}/maniud-install.XXXXXX")"
trap 'rm -rf "$workdir"' EXIT

gh release download "$tag" --repo "$repo" --dir "$workdir" \
  --pattern "$artifact" --pattern SHA256SUMS --pattern "$bundle"
cd "$workdir"

for subject in "$artifact" SHA256SUMS; do
  gh attestation verify "$subject" \
    --repo "$repo" \
    --bundle "$bundle" \
    --signer-workflow "$repo/.github/workflows/release.yml" \
    --source-ref refs/heads/master \
    --deny-self-hosted-runners
done

expected="$(awk -v file="$artifact" '
  $2 == file { digest = $1; count++ }
  END { if (count != 1) exit 1; print digest }
' SHA256SUMS)"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$artifact" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$artifact" | awk '{print $1}')"
fi
test "$actual" = "$expected"

install -d "$HOME/.local/bin"
install -m 0755 "$artifact" "$HOME/.local/bin/maniud"
"$HOME/.local/bin/maniud" --version
```

Add `$HOME/.local/bin` to `PATH` if your shell does not already include it. Stop if any command fails.

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
