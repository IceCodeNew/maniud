package compose

import (
	"encoding/binary"
	"reflect"
	"slices"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const effectiveWorkloadVersion = 1

// Workload projects one active service into runtime-neutral desired state.
func (project Project) Workload(serviceName string) (domain.DesiredWorkload, error) {
	if project.value == nil || projectUsesUnsupportedFields(project.value) {
		return domain.DesiredWorkload{}, ErrInvalidSource
	}

	selected, serviceFound := selectedService(project.value, serviceName)
	if !serviceFound || serviceUsesUnsupportedFields(selected) || !validContainerName(selected.ContainerName) ||
		selected.NetworkMode != "bridge" {
		return domain.DesiredWorkload{}, ErrInvalidSource
	}

	referenceDigest, digestFound := pinnedImageDigest(selected.Image)
	if !digestFound {
		return domain.DesiredWorkload{}, ErrInvalidSource
	}

	workload := domain.DesiredWorkload{
		ServiceName:     selected.Name,
		ContainerName:   selected.ContainerName,
		Image:           selected.Image,
		ReferenceDigest: referenceDigest,
		Entrypoint:      slices.Clone(selected.Entrypoint),
		Command:         slices.Clone(selected.Command),
		SourceDigest:    project.sourceDigest,
		EffectiveDigest: domain.Digest{},
	}
	workload.EffectiveDigest = domain.Hash(effectiveWorkloadBytes(workload))

	return workload, nil
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

func projectUsesUnsupportedFields(project *composetypes.Project) bool {
	return len(project.Networks) != 0 || len(project.Volumes) != 0 || len(project.Secrets) != 0 ||
		len(project.Configs) != 0 || len(project.Models) != 0 || len(project.Extensions) != 0
}

func serviceUsesUnsupportedFields(service composetypes.ServiceConfig) bool {
	value := reflect.ValueOf(service)
	valueType := value.Type()

	for index := range valueType.NumField() {
		if supportedServiceField(valueType.Field(index).Name) {
			continue
		}

		if !value.Field(index).IsZero() {
			return true
		}
	}

	return false
}

func supportedServiceField(name string) bool {
	switch name {
	case "Command", "ContainerName", "Entrypoint", "Image", "Name", "NetworkMode", "Profiles":
		return true
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

func effectiveWorkloadBytes(workload domain.DesiredWorkload) []byte {
	encoded := []byte{effectiveWorkloadVersion}
	encoded = appendString(encoded, workload.ServiceName)
	encoded = appendString(encoded, workload.ContainerName)
	encoded = appendString(encoded, workload.Image)
	encoded = append(encoded, workload.ReferenceDigest[:]...)
	encoded = appendStringSlice(encoded, workload.Entrypoint)
	encoded = appendStringSlice(encoded, workload.Command)

	return encoded
}

func appendString(encoded []byte, value string) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(value)))

	return append(encoded, value...)
}

func appendStringSlice(encoded []byte, values []string) []byte {
	if values == nil {
		return append(encoded, 0)
	}

	encoded = append(encoded, 1)

	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = appendString(encoded, value)
	}

	return encoded
}
