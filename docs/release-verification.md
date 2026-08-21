# Release verification

The Release workflow runs manually from `master`. Before it builds anything,
it verifies that its exact source SHA is still the remote `master` head and
that the Checks workflow has a successful `push` run for that same SHA. It does
not rerun tests when a tag is created.

The workflow calculates the next stable SemVer tag from existing
`vMAJOR.MINOR.PATCH` tags and the selected major, minor, or patch bump. The tag
points directly to the tested master commit; publication does not add a release
commit.

## Platform qualification

| Artifact suffix | Build runner | Native release smoke |
| --- | --- | --- |
| `linux_amd64` | Linux amd64 | yes |
| `linux_arm64` | Linux arm64 | yes |
| `darwin_arm64` | macOS arm64 | yes |
| `darwin_amd64` | Linux amd64 cross-build | no |

All four binaries use `CGO_ENABLED=0`. Checks runs tests and race/branch gates
on master before release. The Darwin amd64 artifact has compile-only,
theoretical support until a native Intel macOS gate exists.

## Assets

For version `X.Y.Z`, a release contains:

```text
maniud_X.Y.Z_linux_amd64
maniud_X.Y.Z_linux_arm64
maniud_X.Y.Z_darwin_arm64
maniud_X.Y.Z_darwin_amd64
SHA256SUMS
maniud_X.Y.Z.sigstore.json
```

The Sigstore bundle contains one SLSA build-provenance attestation whose
subjects are the four binaries and `SHA256SUMS`. GitHub Actions OIDC signs the
attestation. The workflow has no stored signing key.

## Verify a download

Set the downloaded filename, then compare its SHA-256 digest with the exact
entry in `SHA256SUMS`:

```sh
artifact=maniud_X.Y.Z_linux_amd64
expected="$(awk -v file="$artifact" '$2 == file { print $1 }' SHA256SUMS)"
actual="$(shasum -a 256 "$artifact" | awk '{ print $1 }')"
test -n "$expected" && test "$actual" = "$expected"
```

Verify that the artifact attestation came from this repository's Release
workflow and matches the downloaded bytes:

```sh
gh attestation verify "$artifact" \
  --repo IceCodeNew/maniud \
  --signer-workflow IceCodeNew/maniud/.github/workflows/release.yml \
  --source-ref refs/heads/master \
  --deny-self-hosted-runners \
  --bundle maniud_X.Y.Z.sigstore.json
```

Verify the checksum file with the same bundle before trusting checksum entries:

```sh
gh attestation verify SHA256SUMS \
  --repo IceCodeNew/maniud \
  --signer-workflow IceCodeNew/maniud/.github/workflows/release.yml \
  --source-ref refs/heads/master \
  --deny-self-hosted-runners \
  --bundle maniud_X.Y.Z.sigstore.json
```

Reject a release when the attestation identity, workflow path, subject digest,
or checksum differs. Also confirm the release tag resolves to the master SHA
whose Checks run is linked from the Release workflow preflight log.
