package docker

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

const (
	dockerExecutionEvidenceVersion = 1
	dockerNetworkMode              = "bridge"
	dockerOperatingSystem          = "linux"
	dockerArchitectureAMD64        = "amd64"
	dockerArchitectureARM64        = "arm64"
	dockerARM64Variant             = "v8"
)

var (
	// ErrUnsupportedWorkload reports desired state outside the Docker adapter's
	// current platform and workload contract.
	ErrUnsupportedWorkload = errors.New("docker workload is unsupported")
)

var _ application.Runtime = (*Client)(nil)

// Inspect returns Docker daemon identity and platform evidence for apply
// planning.
func (client *Client) Inspect(ctx context.Context) (application.RuntimeEvidence, error) {
	var empty application.RuntimeEvidence

	daemon, err := client.InspectDaemon(ctx)
	if err != nil {
		return empty, err
	}

	platform, valid := dockerPlatform(daemon.OS, daemon.Architecture)
	if !valid {
		return empty, ErrUnsupportedWorkload
	}

	return application.RuntimeEvidence{
		Kind:     domain.RuntimeDocker,
		Platform: platform,
		Digest:   dockerExecutionDigest(daemon),
	}, nil
}

// CheckWorkload validates one projected workload against the negotiated
// Docker daemon and the phase-one container contract.
func (client *Client) CheckWorkload(workload domain.DesiredWorkload) error {
	if client == nil || !validNegotiatedVersion(client.version) ||
		!validDockerWorkload(client.version, workload) {
		return ErrUnsupportedWorkload
	}

	return nil
}

// ObserveWorkload maps one Docker container probe into runtime-neutral apply
// evidence without producing a runtime effect.
func (client *Client) ObserveWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
) (application.WorkloadObservation, error) {
	var empty application.WorkloadObservation

	err := client.CheckWorkload(workload)
	if err != nil {
		return empty, err
	}

	probe, err := client.ProbeContainer(ctx, workload.ContainerName)
	if err != nil {
		return empty, err
	}

	return workloadObservation(probe, workload)
}

func dockerExecutionDigest(daemon Daemon) domain.Digest {
	evidence := []byte{dockerExecutionEvidenceVersion}
	evidence = appendExecutionString(evidence, domain.RuntimeDocker.String())
	evidence = appendExecutionString(evidence, daemon.ID)
	evidence = appendExecutionString(evidence, daemon.Driver)
	evidence = appendExecutionString(evidence, daemon.OS)
	evidence = appendExecutionString(evidence, daemon.Architecture)

	if daemon.Rootless {
		evidence = append(evidence, 1)
	} else {
		evidence = append(evidence, 0)
	}

	return domain.Hash(evidence)
}

func appendExecutionString(encoded []byte, value string) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(value)))

	return append(encoded, value...)
}

func validNegotiatedVersion(version Version) bool {
	protocol, valid := parseAPIVersion(version.Protocol)
	negotiated, compatible := compatibleAPIVersion(protocol)
	_, platformSupported := dockerPlatform(version.OS, version.Architecture)

	return valid && compatible && negotiated == protocol && platformSupported
}

func validDockerWorkload(version Version, workload domain.DesiredWorkload) bool {
	return validOwnershipName(workload.ServiceName) && validContainerName(workload.ContainerName) &&
		validDockerImage(version, workload.Image) && validWorkloadDigests(workload) &&
		validProcessArguments(workload.Entrypoint) && validProcessArguments(workload.Command)
}

func validDockerImage(version Version, image domain.ImageIdentity) bool {
	reference, err := imageref.Parse(image.Reference)
	if err != nil {
		return false
	}

	emptyDigest := domain.Digest{}
	platform, valid := dockerPlatform(version.OS, version.Architecture)

	return valid && reference.Digest() == image.ReferenceDigest && image.Platform == platform &&
		image.PlatformManifest != emptyDigest && image.ImageConfig != emptyDigest
}

func dockerPlatform(osName, architecture string) (domain.Platform, bool) {
	var empty domain.Platform

	if osName != dockerOperatingSystem {
		return empty, false
	}

	switch architecture {
	case dockerArchitectureAMD64:
		return domain.Platform{OS: osName, Architecture: architecture, Variant: ""}, true
	case dockerArchitectureARM64:
		return domain.Platform{OS: osName, Architecture: architecture, Variant: dockerARM64Variant}, true
	default:
		return empty, false
	}
}

func validWorkloadDigests(workload domain.DesiredWorkload) bool {
	emptyDigest := domain.Digest{}

	return workload.SourceDigest != emptyDigest && workload.EffectiveDigest != emptyDigest
}

func validProcessArguments(arguments []string) bool {
	for _, argument := range arguments {
		if !utf8.ValidString(argument) || strings.IndexByte(argument, 0) >= 0 {
			return false
		}
	}

	return true
}

func workloadObservation(
	probe ContainerProbe,
	workload domain.DesiredWorkload,
) (application.WorkloadObservation, error) {
	var empty application.WorkloadObservation

	switch probe.State {
	case ContainerProbeMissing:
		var ownership domain.WorkloadOwnership

		return application.WorkloadObservation{
			State:                application.WorkloadObservationMissing,
			ConfigurationMatches: false,
			Running:              false,
			Ownership:            ownership,
		}, nil
	case ContainerProbeObserved:
		expectation := ContainerExpectation{
			ID:               "",
			Name:             workload.ContainerName,
			ImageReference:   workload.Image.Reference,
			ImageConfig:      workload.Image.ImageConfig,
			PlatformManifest: workload.Image.PlatformManifest,
			Entrypoint:       workload.Entrypoint,
			Command:          workload.Command,
			NetworkMode:      dockerNetworkMode,
			Service:          "",
			Transaction:      "",
			DesiredState:     domain.Digest{},
			Reference:        domain.Digest{},
			AllowedStates:    nil,
		}

		return application.WorkloadObservation{
			State:                application.WorkloadObservationPresent,
			ConfigurationMatches: probe.Container.matchesConfiguration(expectation),
			Running:              probe.Container.Running,
			Ownership:            probe.Container.Ownership,
		}, nil
	case ContainerProbeUnknown:
		return empty, ErrProtocol
	default:
		return empty, ErrProtocol
	}
}
