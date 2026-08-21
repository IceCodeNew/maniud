package compose

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"

	containercompose "github.com/IceCodeNew/maniud/containerconfig/compose"
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
		extensions = serviceExtensions(workload.ServiceName, map[string]any{runtimeMetadataField: runtimeName})
	}

	rendered, err := containercompose.Encode(ctx, workload, containercompose.EncodeOptions{
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
	extensions := serviceExtensions(name, map[string]any{archiveImageSourceKey: runtimeArchiveMetadata(analysis)})
	rendered, err := containercompose.Encode(ctx, workload, containercompose.EncodeOptions{
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

func serviceExtensions(serviceName string, service map[string]any) map[string]any {
	return map[string]any{archiveExtensionKey: map[string]any{
		archiveServicesField: map[string]any{serviceName: service},
	}}
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

func runtimeArchiveMetadata(analysis imagearchive.Analysis) map[string]any {
	source := map[string]any{
		archiveKindField: archiveKind, archiveSelectorField: analysis.Source.Selector(),
		archiveDigestField:         analysis.ArchiveDigest.String(),
		archiveSizeField:           analysis.ArchiveSize,
		archiveManifestField:       analysis.ManifestDigest.String(),
		archiveMemberIndexField:    analysis.MemberIndex,
		archivePlatformField:       containercompose.FormatPlatform(analysis.Identity.Platform),
		archiveReferenceField:      analysis.Identity.ReferenceDigest.String(),
		archivePlatformDigestField: analysis.Identity.PlatformManifest.String(),
		archiveImageConfigField:    analysis.Identity.ImageConfig.String(),
	}
	if analysis.SourceReference != "" {
		source[archiveSourceRefField] = analysis.SourceReference
	}

	return source
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
