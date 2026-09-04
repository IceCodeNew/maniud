package application

import (
	"errors"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	defaultHealthPollInterval = 2 * time.Second
	minimumHealthPollInterval = time.Second
	maximumHealthPollInterval = 10 * time.Second
	healthPollCadenceDivisor  = 2
)

var (
	// ErrHealthPending reports a trusted running or restarting workload whose
	// active healthcheck has not converged yet. The transaction remains active.
	ErrHealthPending = errors.New("health_pending")
	// ErrHealthDegraded reports a workload that failed the health convergence
	// contract. The workload remains untouched for an explicit operator choice.
	ErrHealthDegraded = errors.New("health_degraded")
)

// WorkloadHealthStatus is the bounded runtime-neutral health state from one
// inspect snapshot.
type WorkloadHealthStatus string

const (
	// WorkloadHealthUnknown means an active healthcheck could not be observed.
	WorkloadHealthUnknown WorkloadHealthStatus = "unknown"
	// WorkloadHealthAbsent means the workload has no active healthcheck.
	WorkloadHealthAbsent WorkloadHealthStatus = "absent"
	// WorkloadHealthStarting means the runtime has not reached a health verdict.
	WorkloadHealthStarting WorkloadHealthStatus = "starting"
	// WorkloadHealthHealthy means the runtime reports a passing healthcheck.
	WorkloadHealthHealthy WorkloadHealthStatus = "healthy"
	// WorkloadHealthUnhealthy means the runtime reports a failing healthcheck.
	WorkloadHealthUnhealthy WorkloadHealthStatus = "unhealthy"
)

// WorkloadHealth contains only bounded health metadata. Runtime health command
// output is deliberately excluded.
type WorkloadHealth struct {
	Status        WorkloadHealthStatus
	FailingStreak uint32
}

func validWorkloadHealth(health WorkloadHealth) bool {
	switch health.Status {
	case WorkloadHealthUnknown, WorkloadHealthAbsent:
		return health.FailingStreak == 0
	case WorkloadHealthStarting, WorkloadHealthHealthy, WorkloadHealthUnhealthy:
		return true
	default:
		return false
	}
}

func activeHealthcheck(workload domain.DesiredWorkload) bool {
	return workload.Healthcheck != nil && !workload.Healthcheck.Disabled
}

func healthPollInterval(workload domain.DesiredWorkload, startedAt, capturedAt time.Time) time.Duration {
	if !activeHealthcheck(workload) {
		return 0
	}

	cadence := healthPollCadence(workload.Healthcheck, startedAt, capturedAt)
	if cadence == "" {
		return defaultHealthPollInterval
	}
	parsed, err := time.ParseDuration(cadence)
	if err != nil || parsed <= 0 {
		return 0
	}

	return min(max(parsed/healthPollCadenceDivisor, minimumHealthPollInterval), maximumHealthPollInterval)
}

func healthPollCadence(health *domain.Healthcheck, startedAt, capturedAt time.Time) string {
	if health.StartPeriod == "" || health.StartInterval == "" ||
		startedAt.IsZero() || capturedAt.IsZero() || startedAt.After(capturedAt) {
		return health.Interval
	}
	startPeriod, err := time.ParseDuration(health.StartPeriod)
	if err != nil || startPeriod <= 0 || capturedAt.Sub(startedAt) >= startPeriod {
		return health.Interval
	}

	return health.StartInterval
}

func requireWorkloadConvergence(
	active bool,
	lifecycle WorkloadLifecycle,
	health WorkloadHealth,
) error {
	if lifecycle == WorkloadLifecycleRestarting {
		return ErrHealthPending
	}
	if lifecycle != WorkloadLifecycleRunning || !validWorkloadHealth(health) {
		return ErrHealthDegraded
	}
	if !active {
		if health.Status == WorkloadHealthAbsent {
			return nil
		}

		return ErrHealthDegraded
	}

	if health.Status == WorkloadHealthHealthy {
		return nil
	}
	if health.Status == WorkloadHealthStarting {
		return ErrHealthPending
	}

	return ErrHealthDegraded
}
