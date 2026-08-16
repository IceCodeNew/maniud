package application

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	imagePullActionKind     = "image.pull"
	imageEffectDigestFormat = 1
	imageEffectIntent       = 0
	imageEffectObserved     = 1
	imageEffectMissing      = 2
)

// ImageProbeState separates proven absence and observation from an unknown
// zero value.
type ImageProbeState uint8

const (
	// ImageProbeUnknown is valid only alongside an adapter error.
	ImageProbeUnknown ImageProbeState = iota
	// ImageProbeMissing proves the exact digest-pinned platform was absent.
	ImageProbeMissing
	// ImageProbeObserved carries a verified local image identity.
	ImageProbeObserved
)

// ImageEvidence is runtime-neutral evidence for one local digest-pinned image.
type ImageEvidence struct {
	ReferenceDigest  domain.Digest
	PlatformManifest domain.Digest
	ImageConfig      domain.Digest
	Platform         domain.Platform
}

// ImageProbe is one read-only image-presence conclusion.
type ImageProbe struct {
	State ImageProbeState
	Image ImageEvidence
}

// Matches reports whether the probe proves the complete resolved identity.
func (probe ImageProbe) Matches(expected domain.ImageIdentity) bool {
	return probe.State == ImageProbeObserved &&
		probe.Image.ReferenceDigest == expected.ReferenceDigest &&
		probe.Image.PlatformManifest == expected.PlatformManifest &&
		probe.Image.ImageConfig == expected.ImageConfig && probe.Image.Platform == expected.Platform
}

// ImageRuntime performs and independently observes one immutable image pull.
type ImageRuntime interface {
	PullImage(
		ctx context.Context,
		expected domain.ImageIdentity,
		authenticator credential.Provider,
	) error
	ProbeImage(ctx context.Context, expected domain.ImageIdentity) (ImageProbe, error)
}

type imagePullEffect struct {
	runtime       ImageRuntime
	authenticator credential.Provider
	expected      domain.ImageIdentity
}

func runImagePull(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	expected domain.ImageIdentity,
	runtime ImageRuntime,
	authenticator credential.Provider,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	if runtime == nil || authenticator == nil || sequence <= 0 || !validImageEffectIdentity(expected) {
		return empty, ErrInvalidRequest
	}

	intent := store.ActionIntent{
		Sequence:     sequence,
		Kind:         imagePullActionKind,
		IntentDigest: imageEffectDigest(imageEffectIntent, expected),
	}
	effect := imagePullEffect{
		runtime:       runtime,
		authenticator: authenticator,
		expected:      expected,
	}

	return runRuntimeEffect(ctx, journal, identifier, intent, effect)
}

func (effect imagePullEffect) Apply(ctx context.Context) error {
	err := effect.runtime.PullImage(ctx, effect.expected, effect.authenticator)
	if err != nil {
		return fmt.Errorf("pull runtime image: %w", err)
	}

	return nil
}

func (effect imagePullEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition

	probe, err := effect.runtime.ProbeImage(ctx, effect.expected)
	if err != nil {
		return empty, fmt.Errorf("probe runtime image: %w", err)
	}

	switch probe.State {
	case ImageProbeMissing:
		if probe.Image != emptyImageEvidence() {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest:    imageEffectDigest(imageEffectMissing, effect.expected),
			Satisfied: false,
		}, nil
	case ImageProbeObserved:
		if !probe.Matches(effect.expected) {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest:    imageEffectDigest(imageEffectObserved, effect.expected),
			Satisfied: true,
		}, nil
	case ImageProbeUnknown:
		return empty, ErrConflictingState
	default:
		return empty, ErrConflictingState
	}
}

func validImageEffectIdentity(image domain.ImageIdentity) bool {
	reference, err := imageref.Parse(image.Reference)
	empty := domain.Digest{}

	return err == nil && reference.Digest() == image.ReferenceDigest &&
		image.ReferenceDigest != empty && image.PlatformManifest != empty && image.ImageConfig != empty &&
		image.Platform.OS != "" && image.Platform.Architecture != ""
}

func emptyImageEvidence() ImageEvidence {
	return ImageEvidence{
		ReferenceDigest:  domain.Digest{},
		PlatformManifest: domain.Digest{},
		ImageConfig:      domain.Digest{},
		Platform:         domain.Platform{OS: "", Architecture: "", Variant: ""},
	}
}

func imageEffectDigest(state byte, image domain.ImageIdentity) domain.Digest {
	value := []byte{imageEffectDigestFormat, state}
	value = appendImageEffectString(value, imagePullActionKind)
	value = appendImageEffectString(value, image.Reference)
	value = append(value, image.ReferenceDigest[:]...)
	value = appendImageEffectString(value, image.Platform.OS)
	value = appendImageEffectString(value, image.Platform.Architecture)
	value = appendImageEffectString(value, image.Platform.Variant)
	value = append(value, image.PlatformManifest[:]...)
	value = append(value, image.ImageConfig[:]...)

	return domain.Hash(value)
}

func appendImageEffectString(encoded []byte, value string) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(value)))

	return append(encoded, value...)
}
