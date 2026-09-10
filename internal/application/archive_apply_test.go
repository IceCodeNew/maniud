//nolint:goconst // Scenario labels remain beside the archive cases they identify.
package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	applicationArchiveDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	applicationManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	applicationConfigDigest   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type archiveApplyRuntime struct {
	testRuntime

	probe    ImageProbe
	probeErr error
}

func (runtime *archiveApplyRuntime) ProbeImage(
	context.Context,
	domain.ImageIdentity,
) (ImageProbe, error) {
	return runtime.probe, runtime.probeErr
}

func TestResolveDesiredArchiveImageRequiresExactRuntimeProof(t *testing.T) {
	t.Parallel()

	input, identity := archiveApplyInput(t)
	testArchiveImageProofs(t, input, identity)
}

func testArchiveImageProofs(t *testing.T, input compose.ImageInput, identity domain.ImageIdentity) {
	t.Helper()

	for _, test := range archiveImageProofCases(identity) {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runtime := &archiveApplyRuntime{probe: test.probe, probeErr: test.probeErr}
			service := newService(nil, runtime, nil, nil)
			resolved, err := service.resolveDesiredImage(context.Background(), input, identity.Platform)
			if !errors.Is(err, test.want) {
				t.Fatalf("resolveDesiredImage() error = %v, want %v", err, test.want)
			}
			if test.want == nil && !reflect.DeepEqual(resolved, identity) {
				t.Fatalf("resolveDesiredImage() = %#v, want %#v", resolved, identity)
			}
		})
	}
}

type archiveImageProofCase struct {
	name     string
	probe    ImageProbe
	probeErr error
	want     error
}

func archiveImageProofCases(identity domain.ImageIdentity) []archiveImageProofCase {
	tests := []archiveImageProofCase{
		{name: "observed", probe: observedImageProbe(identity)},
		{
			name:  testMissingValue,
			probe: ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()},
			want:  ErrArchiveImageMissing,
		},
		{
			name: "missing with evidence",
			probe: ImageProbe{
				State: ImageProbeMissing,
				Image: imageEvidence(identity, nil),
			},
			want: ErrConflictingState,
		},
		{
			name: "identity drift",
			probe: ImageProbe{
				State: ImageProbeObserved,
				Image: imageEvidence(identity, func(value *ImageEvidence) {
					value.ImageConfig = domain.Hash([]byte("other config"))
				}),
			},
			want: ErrConflictingState,
		},
		{
			name:  eventUnknown,
			probe: ImageProbe{State: ImageProbeUnknown, Image: emptyImageEvidence()},
			want:  ErrConflictingState,
		},
		{
			name:  "invalid state",
			probe: ImageProbe{State: ImageProbeState(99), Image: emptyImageEvidence()},
			want:  ErrConflictingState,
		},
		{
			name:     "probe failure",
			probe:    ImageProbe{State: ImageProbeUnknown, Image: emptyImageEvidence()},
			probeErr: errTestBoundary,
			want:     errTestBoundary,
		},
	}

	return tests
}

func TestResolveDesiredArchiveImageRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	input, identity := archiveApplyInput(t)
	runtime := &archiveApplyRuntime{probe: observedImageProbe(identity)}
	service := newService(nil, runtime, nil, nil)

	wrongPlatform := identity.Platform
	wrongPlatform.Architecture = "arm64"
	_, err := service.resolveDesiredImage(context.Background(), input, wrongPlatform)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("resolveDesiredImage(platform drift) error = %v", err)
	}

	if _, err := service.resolveDesiredImage(
		context.Background(),
		compose.ImageInput{},
		identity.Platform,
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("resolveDesiredImage(zero input) error = %v", err)
	}

	plain := newService(nil, &testRuntime{}, nil, nil)
	if _, err := plain.proveArchiveImage(context.Background(), identity); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("proveArchiveImage(runtime without image probe) error = %v", err)
	}
}

func archiveApplyInput(t *testing.T) (compose.ImageInput, domain.ImageIdentity) {
	t.Helper()

	source := compose.Source{
		Content: []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/team/archive:1
    network_mode: bridge
    platform: linux/amd64
    pull_policy: never
x-maniud:
  services:
    api:
      image_source:
        kind: docker-archive
        selector: example.com/team/archive:1
        archive_digest: ` + applicationArchiveDigest + `
        archive_size: 10240
        archive_manifest_digest: ` + applicationManifestDigest + `
        archive_member_index: 0
        platform: linux/amd64
        source_reference: example.com/team/archive:1
        reference_digest: ` + applicationManifestDigest + `
        platform_manifest_digest: ` + applicationManifestDigest + `
        image_config_digest: ` + applicationConfigDigest + `
`),
		WorkingDir:  t.TempDir(),
		Environment: nil,
		Profiles:    nil,
	}
	project, err := compose.Load(context.Background(), source)
	if err != nil {
		t.Fatalf("compose.Load() error = %v", err)
	}
	input, err := project.ImageInput("")
	if err != nil {
		t.Fatalf("ImageInput() error = %v", err)
	}
	identity, valid := input.ArchiveIdentity()
	if !valid {
		t.Fatal("ArchiveIdentity() is invalid")
	}

	return input, identity
}
