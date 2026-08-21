package compose

import (
	composetypes "github.com/compose-spec/compose-go/v2/types"

	containercompose "github.com/IceCodeNew/maniud/containerconfig/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func workloadSpecFromService(
	service composetypes.ServiceConfig,
	platform domain.Platform,
	pathFrom string,
	pathTo string,
) (domain.WorkloadSpec, error) {
	spec, err := containercompose.FromService(
		service,
		platform,
		containercompose.PathMapping{From: pathFrom, To: pathTo},
		containercompose.ServiceOptions{AllowPullPolicy: service.PullPolicy != ""},
	)
	if err != nil {
		return domain.WorkloadSpec{}, ErrInvalidSource
	}

	return spec, nil
}
