package compose

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/opencontainers/go-digest"

	composecodec "github.com/IceCodeNew/maniud/containerconfig/compose"
	"github.com/IceCodeNew/maniud/internal/composeext/maniud"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const (
	composeBridgeNetwork = "bridge"
	composeProtocolTCP   = "tcp"
	composeProtocolUDP   = "udp"
)

// RenderRuntime binds a parsed runtime projection to an immutable image, then
// serializes and validates the resulting Compose document in memory.
func RenderRuntime(
	ctx context.Context,
	projection runtimeargv.Projection,
	image domain.ImageIdentity,
	workingDirectory string,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("render runtime Compose: %w", err)
	}
	workload, err := projection.Workload(image)
	if err != nil {
		return nil, fmt.Errorf("bind runtime Compose: %w", err)
	}
	environmentFiles, err := runtimeEnvironmentFiles(projection.EnvironmentFiles(), workingDirectory)
	if err != nil {
		return nil, fmt.Errorf("bind runtime environment files: %w", err)
	}
	var extensions map[string]any
	if runtimeName := projection.Runtime(); runtimeName != "" && runtimeName != composeDockerRuntime {
		extensions = mustEncodeManiudRuntime(
			workload.ServiceName,
			maniud.Runtime(runtimeName),
		)
	}

	rendered, err := composecodec.Encode(ctx, workload, composecodec.EncodeOptions{
		Image: image.Reference, WorkingDirectory: workingDirectory, ProjectName: projection.Name(),
		EnvironmentFiles: environmentFiles, Extensions: extensions,
	})
	if err != nil {
		return nil, fmt.Errorf("render runtime Compose: %w", err)
	}

	return rendered, nil
}

// RenderArchive serializes an analyzed Docker archive through the same
// runtime-neutral workload projection used by registry and argv generation.
func RenderArchive(
	ctx context.Context,
	analysis imagearchive.Analysis,
	explicitName string,
	workingDirectory string,
) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", fmt.Errorf("render archive Compose: %w", err)
	}
	name, err := analysis.ServiceName(explicitName)
	if err != nil {
		return nil, "", fmt.Errorf("select archive service: %w", err)
	}
	workload := domain.ResolveWorkloadSpec(domain.WorkloadSpec{
		ServiceName: name, ContainerName: name, Platform: analysis.Identity.Platform,
		NetworkMode: composeBridgeNetwork,
	}, analysis.Identity)
	proof := runtimeArchiveProof(analysis)
	extensions, err := encodeManiudService(
		name,
		maniud.Service{Runtime: maniud.RuntimeDocker, ArchiveProof: &proof},
	)
	if err != nil {
		return nil, "", fmt.Errorf("encode archive extension: %w", err)
	}
	rendered, err := composecodec.Encode(ctx, workload, composecodec.EncodeOptions{
		Image: analysis.ComposeReference, WorkingDirectory: workingDirectory, ProjectName: name,
		PullPolicy: "never", Extensions: extensions,
	})
	if err != nil {
		return nil, "", fmt.Errorf("render archive Compose: %w", err)
	}
	err = validateRenderedArchive(ctx, rendered, workingDirectory, name, workload)
	if err != nil {
		return nil, "", err
	}

	return rendered, name, nil
}

func validateRenderedArchive(
	ctx context.Context,
	rendered []byte,
	workingDirectory string,
	name string,
	expected domain.WorkloadSpec,
) error {
	project, err := Load(ctx, Source{
		Content: rendered, WorkingDir: workingDirectory, Environment: map[string]string{},
	})
	if err != nil {
		return fmt.Errorf("validate generated archive Compose: %w", err)
	}
	input, err := project.ImageInput(name)
	if err != nil {
		return fmt.Errorf("validate generated archive image: %w", err)
	}
	image, valid := input.ArchiveIdentity()
	if !valid {
		return ErrInvalidSource
	}
	workload, err := project.Workload(name, image)
	if err != nil || !reflect.DeepEqual(workload.WorkloadSpec, expected) {
		return ErrInvalidSource
	}

	return nil
}

func runtimeArchiveProof(analysis imagearchive.Analysis) maniud.ArchiveProof {
	return maniud.ArchiveProof{
		ArchiveDigest: digest.Digest(analysis.ArchiveDigest.String()), ArchiveSize: analysis.ArchiveSize,
		ManifestDigest: digest.Digest(analysis.ManifestDigest.String()), MemberIndex: analysis.MemberIndex,
		Platform: analysis.Identity.Platform, Selector: analysis.Source.Selector(),
		SourceReference:        analysis.SourceReference,
		ReferenceDigest:        digest.Digest(analysis.Identity.ReferenceDigest.String()),
		PlatformManifestDigest: digest.Digest(analysis.Identity.PlatformManifest.String()),
		ImageConfigDigest:      digest.Digest(analysis.Identity.ImageConfig.String()),
	}
}

func runtimeEnvironmentFiles(values []string, workingDirectory string) ([]string, error) {
	if values == nil {
		return nil, nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		relative, err := filepath.Rel(workingDirectory, value)
		if err != nil || relative == "." || filepath.IsAbs(relative) {
			return nil, runtimeargv.ErrInvalid
		}
		result[index] = filepath.ToSlash(relative)
	}

	return result, nil
}
