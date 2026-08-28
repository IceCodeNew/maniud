package docker

import (
	containerdocker "github.com/IceCodeNew/maniud/containerconfig/docker"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func dockerConfigurationMatches(observed domain.WorkloadSpec, expected domain.WorkloadSpec) bool {
	return containerdocker.Equivalent(observed, expected)
}
