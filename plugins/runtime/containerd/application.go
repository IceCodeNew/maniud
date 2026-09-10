package containerd

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
)

const (
	containerdExecutionEvidenceVersion = 1
	containerdConfigurationVersion     = 1
)

var (
	// ErrUnsupportedWorkload reports desired state outside the native
	// containerd workload contract.
	ErrUnsupportedWorkload = errors.New("containerd workload is unsupported")
)

var (
	_ application.Runtime                   = (*Client)(nil)
	_ application.ImageRuntime              = (*Client)(nil)
	_ application.WorkloadEffectRuntime     = (*Client)(nil)
	_ application.WorkloadStartRuntime      = (*Client)(nil)
	_ application.WorkloadTransitionRuntime = (*Client)(nil)
	_ application.WorkloadDiscardRuntime    = (*Client)(nil)
	_ application.WorkloadArchiveRuntime    = (*Client)(nil)
)

// Inspect returns native runtime, snapshotter, platform, and CNI identity
// evidence while retaining the endpoint identity checks around the operation.
func (client *Client) Inspect(ctx context.Context) (application.RuntimeEvidence, error) {
	var info workloadRuntimeInfo
	if client == nil || client.workloads == nil {
		return application.RuntimeEvidence{}, ErrUnavailable
	}
	err := client.checked(ctx, func(requestContext context.Context) error {
		var err error
		info, err = client.workloads.Inspect(requestContext)

		return wrapWorkloadBackendError("inspection", err)
	})
	if err != nil {
		return application.RuntimeEvidence{}, workloadError(err)
	}
	if !validWorkloadRuntimeInfo(info) {
		return application.RuntimeEvidence{}, ErrProtocol
	}

	return application.RuntimeEvidence{
		Kind:     domain.RuntimeContainerd,
		Platform: info.Platform,
		Digest:   containerdExecutionDigest(client.scope, info),
	}, nil
}

// CheckWorkload validates one desired workload against the lossless public
// containerd projection. Host and daemon capabilities are rechecked by each
// effect under the endpoint identity fence.
func (client *Client) CheckWorkload(workload domain.DesiredWorkload) error {
	if client == nil || client.workloads == nil || !validContainerdWorkload(workload) {
		return ErrUnsupportedWorkload
	}

	return nil
}

// ObserveWorkload reads one exact logical container name without producing a
// workload effect.
func (client *Client) ObserveWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
) (application.WorkloadObservation, error) {
	if err := client.CheckWorkload(workload); err != nil {
		return application.WorkloadObservation{}, err
	}

	var candidates workloadCandidates
	err := client.checked(ctx, func(requestContext context.Context) error {
		var err error
		candidates, err = client.workloads.Candidates(requestContext, workload.ContainerName, "", "")

		return wrapWorkloadBackendError("candidate inspection", err)
	})
	if err != nil {
		return application.WorkloadObservation{}, workloadError(err)
	}
	if candidates.Owned != nil || candidates.Named == nil {
		if candidates.Owned != nil {
			return application.WorkloadObservation{}, ErrProtocol
		}

		return missingWorkloadObservation(), nil
	}

	evidence, err := workloadEffectEvidence(candidates.Named, workload)
	if err != nil {
		return application.WorkloadObservation{}, err
	}

	return application.WorkloadObservation{
		ID:                   evidence.ID,
		State:                application.WorkloadObservationPresent,
		ConfigurationDigest:  evidence.ConfigurationDigest,
		StorageDigest:        evidence.StorageDigest,
		RuntimeMounts:        slices.Clone(evidence.RuntimeMounts),
		ConfigurationMatches: evidence.ConfigurationMatches,
		Lifecycle:            evidence.Lifecycle,
		Health:               evidence.Health,
		Ownership:            evidence.Ownership,
	}, nil
}

// PullImage is deliberately unsupported because containerd's core API has no
// registry transport. A preloaded exact image is accepted by ProbeImage.
func (*Client) PullImage(context.Context, domain.ImageIdentity, credential.Provider) error {
	return ErrUnsupportedWorkload
}

// ProbeImage proves one exact preloaded image through the existing complete
// local image-graph verifier.
func (client *Client) ProbeImage(
	ctx context.Context,
	expected domain.ImageIdentity,
) (application.ImageProbe, error) {
	source, valid := normalizedContainerdImage(expected)
	if client == nil || !valid {
		return application.ImageProbe{}, ErrUnsupportedWorkload
	}
	resolved, err := client.Resolve(ctx, source, expected.Platform)
	if errors.Is(err, registry.ErrNotFound) {
		return application.ImageProbe{State: application.ImageProbeMissing}, nil
	}
	if err != nil {
		return application.ImageProbe{}, err
	}

	return application.ImageProbe{
		State: application.ImageProbeObserved,
		Image: application.ImageEvidence{
			ReferenceDigest:  resolved.ReferenceDigest,
			PlatformManifest: resolved.PlatformManifest,
			ImageConfig:      resolved.ImageConfig,
			Platform:         resolved.Platform,
		},
	}, nil
}

func validWorkloadRuntimeInfo(info workloadRuntimeInfo) bool {
	return validContainerdPlatform(info.Platform) && info.Runtime != "" && info.Snapshotter != "" &&
		info.NetworkDigest != (domain.Digest{})
}

func validContainerdWorkload(workload domain.DesiredWorkload) bool {
	if containerdconfig.Validate(workload.WorkloadSpec) != nil || !validContainerdImage(workload.Image) ||
		workload.Platform != workload.Image.Platform || workload.SourceDigest == (domain.Digest{}) ||
		workload.EffectiveDigest == (domain.Digest{}) ||
		workload.EffectiveDigest != domain.ComputeEffectiveDigest(workload) {
		return false
	}

	return !slices.ContainsFunc(workload.Labels, func(value string) bool {
		name, _, _ := strings.Cut(value, "=")

		return domain.IsOwnershipLabel(name) || strings.HasPrefix(name, maniudLabelPrefix) ||
			strings.HasPrefix(name, "containerd.io/restart.")
	})
}

func validContainerdImage(image domain.ImageIdentity) bool {
	_, valid := normalizedContainerdImage(image)

	return valid
}

//nolint:cyclop // Image normalization verifies every independent pinned identity field.
func normalizedContainerdImage(image domain.ImageIdentity) (imageref.Source, bool) {
	if image.Reference == "" || image.ReferenceDigest == (domain.Digest{}) ||
		image.PlatformManifest == (domain.Digest{}) || image.ImageConfig == (domain.Digest{}) ||
		!validContainerdPlatform(image.Platform) || image.Origin != domain.ImageOriginRegistry {
		return imageref.Source{}, false
	}
	source, err := imageref.Normalize(image.Reference)
	if err != nil || source.String() != image.Reference {
		return imageref.Source{}, false
	}

	reference, err := imageref.Parse(image.Reference)

	return source, err == nil && source.IsPinned() && reference.Digest() == image.ReferenceDigest
}

func validContainerdPlatform(platform domain.Platform) bool {
	return platform.OS == containerdPlatformOS &&
		(platform.Architecture == containerdArchitectureAMD64 && platform.Variant == "" ||
			platform.Architecture == containerdArchitectureARM64 && platform.Variant == "v8")
}

func containerdExecutionDigest(scope runtimeScope, info workloadRuntimeInfo) domain.Digest {
	value := []byte{containerdExecutionEvidenceVersion}
	for _, selected := range []string{
		domain.RuntimeContainerd.String(), scope.version, scope.revision, scope.uuid,
		info.Runtime, info.Snapshotter, info.Platform.OS, info.Platform.Architecture, info.Platform.Variant,
	} {
		value = appendContainerdString(value, selected)
	}
	value = binary.AppendUvarint(value, scope.process)
	value = binary.AppendUvarint(value, scope.pidns)
	value = append(value, info.NetworkDigest[:]...)
	if info.Restart {
		value = append(value, 1)
	} else {
		value = append(value, 0)
	}

	return domain.Hash(value)
}

func containerdConfigurationDigest(workload nativeWorkload) domain.Digest {
	value := []byte{containerdConfigurationVersion}
	value = appendContainerdString(value, workload.ImageReference)
	value = append(value, workload.ImageConfig[:]...)
	value = append(value, workload.PlatformManifest[:]...)
	spec, err := containerdconfig.Decode(workload.Configuration)
	if err != nil {
		return domain.Digest{}
	}
	configuration := domain.ComputeWorkloadSpecDigest(spec)

	return domain.Hash(append(value, configuration[:]...))
}

func appendContainerdString(value []byte, selected string) []byte {
	value = binary.AppendUvarint(value, uint64(len(selected)))

	return append(value, selected...)
}

func workloadEffectEvidence(
	workload *nativeWorkload,
	desired domain.DesiredWorkload,
) (application.WorkloadEffectEvidence, error) {
	if workload == nil || workload.ID == "" || workload.Name == "" ||
		workload.ConfigurationDigest == (domain.Digest{}) {
		return application.WorkloadEffectEvidence{}, ErrProtocol
	}
	storage, valid := domain.ComputeStorageDigest(desired, workload.RuntimeMounts)
	if !valid {
		return application.WorkloadEffectEvidence{}, ErrProtocol
	}
	decoded, decodeErr := containerdconfig.Decode(workload.Configuration)
	matches := decodeErr == nil && reflect.DeepEqual(decoded, desired.WorkloadSpec) &&
		workload.ImageReference == desired.Image.Reference &&
		workload.ImageConfig == desired.Image.ImageConfig &&
		workload.PlatformManifest == desired.Image.PlatformManifest

	return application.WorkloadEffectEvidence{
		ID: workload.ID, Name: workload.Name,
		ConfigurationDigest: workload.ConfigurationDigest,
		StorageDigest:       storage, RuntimeMounts: slices.Clone(workload.RuntimeMounts),
		ConfigurationMatches: matches, Lifecycle: workload.Lifecycle,
		Health:    application.WorkloadHealth{Status: application.WorkloadHealthAbsent},
		Ownership: workload.Ownership,
	}, nil
}

func missingWorkloadObservation() application.WorkloadObservation {
	return application.WorkloadObservation{
		ID: "", State: application.WorkloadObservationMissing,
		ConfigurationDigest: domain.Digest{}, StorageDigest: domain.Digest{}, RuntimeMounts: nil,
		ConfigurationMatches: false, Lifecycle: application.WorkloadLifecycleUnknown,
		Health: application.WorkloadHealth{}, Ownership: domain.WorkloadOwnership{},
	}
}

func workloadError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrProtocol) ||
		errors.Is(err, ErrUnsupportedWorkload) || errors.Is(err, application.ErrArchivePathMissing) ||
		errors.Is(err, application.ErrArchiveConflict) {
		return err
	}

	return ErrProtocol
}

func wrapWorkloadBackendError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("containerd workload %s: %w", operation, err)
}
