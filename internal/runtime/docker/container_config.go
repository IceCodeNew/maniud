package docker

import (
	"maps"
	"strings"
	"unicode/utf8"

	containerdocker "github.com/IceCodeNew/maniud/containerconfig/docker"
	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const maximumDockerConfigurationTextBytes = 4096

func dockerCreateConfiguration(
	workload domain.DesiredWorkload,
	transaction string,
	options application.WorkloadCreateOptions,
) (containertypes.CreateRequest, bool) {
	labels, valid := dockerLabels(workload.Labels, workloadOwnershipLabels(workload, transaction))
	if !valid {
		return containertypes.CreateRequest{}, false
	}
	spec := workloadSpecWithLabels(workload.WorkloadSpec, labels)
	request, err := containerdocker.Encode(spec, containerdocker.CreateOptions{
		ImageReference: workload.Image.Reference, CopyImageVolumes: options.CopyImageVolumes,
	})
	if err != nil {
		return containertypes.CreateRequest{}, false
	}

	return request, true
}

func dockerConfiguration(
	spec domain.WorkloadSpec,
	image string,
	labels map[string]string,
) (*containertypes.Config, *containertypes.HostConfig, bool) {
	request, err := containerdocker.Encode(workloadSpecWithLabels(spec, labels), containerdocker.CreateOptions{
		ImageReference: image, CopyImageVolumes: true,
	})
	if err != nil {
		return nil, nil, false
	}

	return request.Config, request.HostConfig, true
}

func workloadSpecWithLabels(spec domain.WorkloadSpec, labels map[string]string) domain.WorkloadSpec {
	clone := spec.Clone()
	clone.Labels = make([]string, 0, len(labels))
	for key, value := range labels {
		clone.Labels = append(clone.Labels, key+"="+value)
	}

	return clone
}

func dockerLabels(values []string, ownership map[string]string) (map[string]string, bool) {
	labels := make(map[string]string, len(values)+len(ownership))
	for _, value := range values {
		key, selected, found := strings.Cut(value, "=")
		if !found {
			selected = ""
		}
		if key == "" || !validDockerConfigurationText(key) || !validDockerConfigurationText(selected) ||
			domain.IsOwnershipLabel(key) {
			return nil, false
		}
		if _, exists := labels[key]; exists {
			return nil, false
		}
		labels[key] = selected
	}
	maps.Copy(labels, ownership)

	return labels, true
}

func validDockerConfigurationText(value string) bool {
	return len(value) <= maximumDockerConfigurationTextBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}
