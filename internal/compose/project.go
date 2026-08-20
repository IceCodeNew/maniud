package compose

import (
	"reflect"
	"slices"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

// Runtime returns the runtime provenance for one active service. Plain
// Compose and archive-only metadata use Docker unless x-maniud says otherwise.
func (project Project) Runtime(serviceName string) (domain.RuntimeKind, error) {
	selected, err := project.service(serviceName)
	if err != nil {
		return "", err
	}
	if !project.maniud {
		return domain.RuntimeDocker, nil
	}
	runtimeKind, found := project.runtimes[selected.Name]
	if !found {
		return "", ErrInvalidSource
	}

	return runtimeKind, nil
}

// Workload projects one active service into runtime-neutral desired state.
func (project Project) Workload(
	serviceName string,
	image domain.ImageIdentity,
) (domain.DesiredWorkload, error) {
	selected, err := project.service(serviceName)
	if err != nil || !project.imageResolvesSource(selected, image) {
		return domain.DesiredWorkload{}, ErrInvalidSource
	}

	image = image.Clone()

	spec, err := workloadSpecFromService(selected, image.Platform, project.pathFrom, project.pathTo)
	if err != nil {
		return domain.DesiredWorkload{}, err
	}
	spec.Entrypoint = effectiveProcessArguments(selected.Entrypoint, image.Entrypoint)
	spec.Command = effectiveCommandArguments(selected, image.Command)
	spec = domain.ResolveWorkloadSpec(spec, image)
	workload := domain.DesiredWorkload{
		WorkloadSpec:    spec,
		Image:           image,
		SourceDigest:    project.sourceDigest,
		EffectiveDigest: domain.Digest{},
	}
	workload.EffectiveDigest = domain.ComputeEffectiveDigest(workload)

	return workload, nil
}

func effectiveProcessArguments(override, imageDefault []string) []string {
	if override == nil {
		return slices.Clone(imageDefault)
	}

	return slices.Clone(override)
}

func effectiveCommandArguments(service composetypes.ServiceConfig, imageDefault []string) []string {
	if service.Command != nil {
		return slices.Clone(service.Command)
	}

	if service.Entrypoint != nil {
		return []string{}
	}

	return slices.Clone(imageDefault)
}

func (project Project) service(serviceName string) (composetypes.ServiceConfig, error) {
	if project.value == nil || projectUsesUnsupportedFields(project.value, project.maniud) {
		return composetypes.ServiceConfig{}, ErrInvalidSource
	}

	selected, serviceFound := selectedService(project.value, serviceName)
	archive, isArchive := project.archives[selected.Name]
	if !serviceFound || serviceUsesUnsupportedFields(selected, isArchive) ||
		!validContainerName(selected.ContainerName) ||
		selected.NetworkMode != composeBridgeNetwork {
		return composetypes.ServiceConfig{}, ErrInvalidSource
	}
	if isArchive && !validArchiveService(selected.Image, selected.Platform, selected.PullPolicy, archive) {
		return composetypes.ServiceConfig{}, ErrInvalidSource
	}

	return selected, nil
}

func (project Project) imageResolvesSource(service composetypes.ServiceConfig, image domain.ImageIdentity) bool {
	if archive, found := project.archives[service.Name]; found {
		expected, valid := archiveIdentity(service.Image, service.Platform, service.PullPolicy, archive)

		return valid && sameArchiveIdentity(image, expected)
	}
	if image.Origin != domain.ImageOriginRegistry {
		return false
	}
	if service.Platform != "" {
		platform, valid := archivePlatform(service.Platform)
		if !valid || platform != image.Platform {
			return false
		}
	}

	return imageResolvesSource(service.Image, image)
}

func imageResolvesSource(value string, image domain.ImageIdentity) bool {
	source, err := imageref.Normalize(value)
	if err != nil {
		return false
	}

	expected, err := source.Pin(image.ReferenceDigest)
	if err != nil {
		return false
	}

	return expected.String() == image.Reference
}

func selectedService(project *composetypes.Project, requested string) (composetypes.ServiceConfig, bool) {
	if requested == "" {
		names := project.ServiceNames()
		if len(names) != 1 {
			var service composetypes.ServiceConfig

			return service, false
		}

		requested = names[0]
	}

	service, err := project.GetService(requested)

	return service, err == nil
}

func projectUsesUnsupportedFields(
	project *composetypes.Project,
	maniud bool,
) bool {
	extensionsValid := len(project.Extensions) == 0 && !maniud
	if maniud {
		_, extensionFound := project.Extensions[archiveExtensionKey]
		extensionsValid = len(project.Extensions) == 1 && extensionFound
	}

	return len(project.Networks) != 0 || len(project.Volumes) != 0 || len(project.Secrets) != 0 ||
		len(project.Configs) != 0 || len(project.Models) != 0 || !extensionsValid
}

func serviceUsesUnsupportedFields(service composetypes.ServiceConfig, archive bool) bool {
	value := reflect.ValueOf(service)
	valueType := value.Type()

	for index := range valueType.NumField() {
		if supportedServiceField(valueType.Field(index).Name, archive) {
			continue
		}

		if !value.Field(index).IsZero() {
			return true
		}
	}

	return false
}

func supportedServiceField(name string, archive bool) bool {
	switch name {
	case "BlkioConfig", "CapAdd", "CapDrop", "CgroupParent", "Cgroup", "CPUS", "Command",
		"ContainerName", "Devices", "DNS", "DNSOpts", "DNSSearch", "Entrypoint", "Environment",
		"Expose", "ExtraHosts", "GroupAdd", "HealthCheck", "Hostname", "Image", "Init", "Labels", "MemLimit",
		"Name", "NetworkMode", "OomKillDisable", "OomScoreAdj", "PidsLimit", "Platform", "Ports",
		"Profiles", "ReadOnly", "Restart", "SecurityOpt", "ShmSize", "StdinOpen", "StopGracePeriod",
		"StopSignal", "Sysctls", "Tmpfs", "Tty", "Ulimits", "User", "Volumes", "WorkingDir":
		return true
	case "PullPolicy":
		return archive
	default:
		return false
	}
}

func validContainerName(name string) bool {
	if len(name) == 0 || len(name) > 63 || !lowerAlphaNumeric(name[0]) || !lowerAlphaNumeric(name[len(name)-1]) {
		return false
	}

	for index := 1; index < len(name)-1; index++ {
		if name[index] != '-' && !lowerAlphaNumeric(name[index]) {
			return false
		}
	}

	return true
}

func lowerAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}
