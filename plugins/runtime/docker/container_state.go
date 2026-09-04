package docker

import (
	"math"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

// ContainerState is Docker's typed lifecycle state at one inspect snapshot.
type ContainerState string

const (
	// ContainerCreated has never started.
	ContainerCreated ContainerState = "created"
	// ContainerRunning is executing normally.
	ContainerRunning ContainerState = "running"
	// ContainerPaused is executing with all processes paused.
	ContainerPaused ContainerState = "paused"
	// ContainerRestarting is between an exit and a restart attempt.
	ContainerRestarting ContainerState = "restarting"
	// ContainerRemoving is being deleted.
	ContainerRemoving ContainerState = "removing"
	// ContainerExited has stopped after starting at least once.
	ContainerExited ContainerState = "exited"
	// ContainerDead cannot be restarted without removal.
	ContainerDead ContainerState = "dead"
)

func decodeContainerState(state *containertypes.State) (ContainerState, bool) {
	status := ContainerState(state.Status)

	switch status {
	case ContainerCreated:
		return status, stoppedAliveState(state)
	case ContainerRunning:
		return status, runningState(state)
	case ContainerPaused:
		return status, pausedState(state)
	case ContainerRestarting:
		return status, restartingState(state)
	case ContainerRemoving:
		return status, removingState(state)
	case ContainerExited:
		return status, stoppedAliveState(state)
	case ContainerDead:
		return status, deadState(state)
	default:
		return "", false
	}
}

func stoppedState(state *containertypes.State) bool {
	return !state.Running && !state.Paused && !state.Restarting
}

func stoppedAliveState(state *containertypes.State) bool {
	return stoppedState(state) && !state.Dead
}

func runningState(state *containertypes.State) bool {
	return state.Running && !state.Paused && !state.Restarting && !state.Dead
}

func pausedState(state *containertypes.State) bool {
	return state.Running && state.Paused && !state.Restarting && !state.Dead
}

func restartingState(state *containertypes.State) bool {
	return state.Running && !state.Paused && state.Restarting && !state.Dead
}

func removingState(state *containertypes.State) bool {
	return stoppedState(state)
}

func deadState(state *containertypes.State) bool {
	return stoppedState(state) && state.Dead
}

func dockerWorkloadHealth(
	observed *containertypes.Health,
	configured *domain.Healthcheck,
) (application.WorkloadHealth, bool) {
	active := configured != nil && !configured.Disabled
	if !active {
		return inactiveDockerWorkloadHealth(observed)
	}
	if observed == nil || observed.Status == "" && observed.FailingStreak == 0 {
		return application.WorkloadHealth{Status: application.WorkloadHealthUnknown}, true
	}
	if observed.FailingStreak < 0 || uint64(observed.FailingStreak) > math.MaxUint32 {
		return application.WorkloadHealth{}, false
	}

	status, valid := dockerHealthStatus(observed.Status)
	if !valid {
		return application.WorkloadHealth{}, false
	}

	return application.WorkloadHealth{
		Status: status, FailingStreak: uint32(observed.FailingStreak),
	}, true
}

func inactiveDockerWorkloadHealth(observed *containertypes.Health) (application.WorkloadHealth, bool) {
	if observed != nil && (observed.Status != "" || observed.FailingStreak != 0 || len(observed.Log) != 0) {
		return application.WorkloadHealth{}, false
	}

	return application.WorkloadHealth{Status: application.WorkloadHealthAbsent}, true
}

func dockerHealthStatus(status containertypes.HealthStatus) (application.WorkloadHealthStatus, bool) {
	switch status {
	case containertypes.Starting:
		return application.WorkloadHealthStarting, true
	case containertypes.Healthy:
		return application.WorkloadHealthHealthy, true
	case containertypes.Unhealthy:
		return application.WorkloadHealthUnhealthy, true
	case containertypes.NoHealthcheck:
		return "", false
	default:
		return "", false
	}
}
