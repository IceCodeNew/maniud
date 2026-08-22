# Release verification

The Release workflow runs manually from `master`. Before it builds anything,
it verifies that its exact source SHA is still the remote `master` head and
that the Checks workflow has a successful `push` run for that same SHA. It does
not rerun tests when a tag is created.

The workflow calculates the next stable SemVer tag from existing
`vMAJOR.MINOR.PATCH` tags and the selected major, minor, or patch bump. The tag
points directly to the tested master commit; publication does not add a release
commit. With no existing stable tag, the default patch selection starts the
shared module version at `0.1.0`.

Every public Go module uses that same version and commit. The tested master
revision must already require that version for every dependency between modules
in this repository. Prepare those `go.mod` changes before dispatching Release;
normal master Checks test them. The preflight rejects stale or mixed internal
versions.

Before publishing the root release, the workflow creates a lightweight
`MODULE/vMAJOR.MINOR.PATCH` tag for each tracked nested `go.mod`, in module
dependency order. A retry accepts an existing module tag only when it points
directly to the same tested commit. This includes `containerconfig`, `imageref`,
and each independently importable adapter module. After staging the draft, the
workflow creates or verifies the lightweight root tag against that commit.

The workflow stages a draft release before uploading assets. It validates the
complete nonempty asset set, then publishes the draft as the final visibility
step. If a run stops after creating the draft, a later dispatch from the same
master SHA resumes that draft and replaces its assets. A draft that points to a
different commit blocks publication for operator review. A master commit that
already has a stable root tag cannot start another release.

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
