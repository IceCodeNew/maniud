package podman

import (
	"context"
	"net/http"
	neturl "net/url"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

// DiscardWorkload force-removes one exact transaction-owned workload selected
// by a strict desired-name and ownership probe.
func (client *Client) DiscardWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) error {
	probe, err := client.ProbeDiscardedWorkload(ctx, workload, transaction)
	if err != nil {
		return err
	}
	if probe.State != application.WorkloadEffectProbeObserved ||
		!validStartedWorkload(probe.Workload, workload, transaction, probe.Workload.Lifecycle) ||
		(probe.Workload.Lifecycle != application.WorkloadLifecycleCreated &&
			probe.Workload.Lifecycle != application.WorkloadLifecycleRunning &&
			probe.Workload.Lifecycle != application.WorkloadLifecycleExited) {
		return ErrProtocol
	}
	path := client.apiPath("/containers/" + probe.Workload.ID)
	response, err := client.request( //nolint:bodyclose // decodePodmanRemovalResponse consumes and closes it.
		ctx,
		http.MethodDelete,
		path,
		neturl.Values{"force": {podmanQueryTrue}, "volumes": {podmanQueryFalse}},
		nil,
		false,
	)
	if err != nil {
		return err
	}

	return decodePodmanRemovalResponse(response, probe.Workload.ID)
}

// ProbeDiscardedWorkload proves absence or returns the one exact discard candidate.
func (client *Client) ProbeDiscardedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (application.WorkloadEffectProbe, error) {
	return client.probeWorkloadEffect(ctx, workload, transaction, "")
}
