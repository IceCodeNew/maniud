//nolint:funlen // The convergence table documents the complete health-state matrix.
package application

import (
	"errors"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestWorkloadHealthConvergenceMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		active    bool
		lifecycle WorkloadLifecycle
		health    WorkloadHealth
		want      error
	}{
		{
			name: "active healthy", active: true, lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: WorkloadHealthHealthy},
		},
		{
			name: "active starting", active: true, lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: WorkloadHealthStarting}, want: ErrHealthPending,
		},
		{
			name: "active restarting", active: true, lifecycle: WorkloadLifecycleRestarting,
			health: WorkloadHealth{Status: WorkloadHealthUnknown}, want: ErrHealthPending,
		},
		{
			name: "active unhealthy", active: true, lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: WorkloadHealthUnhealthy, FailingStreak: 4}, want: ErrHealthDegraded,
		},
		{
			name: "active absent", active: true, lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: WorkloadHealthAbsent}, want: ErrHealthDegraded,
		},
		{
			name: "active unobservable", active: true, lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: WorkloadHealthUnknown}, want: ErrHealthDegraded,
		},
		{
			name: "inactive running", lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: WorkloadHealthAbsent},
		},
		{
			name: "inactive contradictory health", lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: WorkloadHealthHealthy}, want: ErrHealthDegraded,
		},
		{
			name: "non-running", active: true, lifecycle: WorkloadLifecycleExited,
			health: WorkloadHealth{Status: WorkloadHealthHealthy}, want: ErrHealthDegraded,
		},
		{
			name: "invalid bounded state", active: true, lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: WorkloadHealthUnknown, FailingStreak: 1}, want: ErrHealthDegraded,
		},
		{
			name: "unknown native status", active: true, lifecycle: WorkloadLifecycleRunning,
			health: WorkloadHealth{Status: "new"}, want: ErrHealthDegraded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := requireWorkloadConvergence(test.active, test.lifecycle, test.health)
			if !errors.Is(err, test.want) || (test.want == nil && err != nil) {
				t.Fatalf("requireWorkloadConvergence() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestHealthPollIntervalUsesHalfCadenceWithinBounds(t *testing.T) {
	t.Parallel()

	const steadyInterval = "20s"

	capturedAt := time.Unix(100, 0).UTC()
	startPeriod := (30 * time.Second).String()
	startInterval := (6 * time.Second).String()
	tests := []struct {
		name      string
		health    *domain.Healthcheck
		startedAt time.Time
		want      time.Duration
	}{
		{name: "absent"},
		{name: "disabled", health: &domain.Healthcheck{Disabled: true}},
		{
			name:   "default",
			health: &domain.Healthcheck{Test: []string{testHealthCommand, testTrueCommand}},
			want:   2 * time.Second,
		},
		{name: "interval", health: &domain.Healthcheck{Interval: "4s"}, want: 2 * time.Second},
		{
			name:      "start interval within start period",
			health:    &domain.Healthcheck{Interval: steadyInterval, StartPeriod: startPeriod, StartInterval: startInterval},
			startedAt: capturedAt.Add(-29 * time.Second),
			want:      3 * time.Second,
		},
		{
			name:      "steady interval at start period boundary",
			health:    &domain.Healthcheck{Interval: steadyInterval, StartPeriod: startPeriod, StartInterval: startInterval},
			startedAt: capturedAt.Add(-30 * time.Second),
			want:      10 * time.Second,
		},
		{
			name:   "missing start uses steady interval",
			health: &domain.Healthcheck{Interval: steadyInterval, StartPeriod: startPeriod, StartInterval: startInterval},
			want:   10 * time.Second,
		},
		{
			name:      "future start uses steady interval",
			health:    &domain.Healthcheck{Interval: steadyInterval, StartPeriod: startPeriod, StartInterval: startInterval},
			startedAt: capturedAt.Add(time.Second),
			want:      10 * time.Second,
		},
		{
			name:   "start interval needs start period",
			health: &domain.Healthcheck{Interval: steadyInterval, StartInterval: startInterval}, want: 10 * time.Second,
		},
		{name: "minimum", health: &domain.Healthcheck{Interval: "500ms"}, want: time.Second},
		{name: "maximum", health: &domain.Healthcheck{Interval: "2m"}, want: 10 * time.Second},
		{name: testInvalidValue, health: &domain.Healthcheck{Interval: testInvalidValue}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := healthPollInterval(
				domain.DesiredWorkload{Healthcheck: test.health},
				test.startedAt,
				capturedAt,
			)
			if got != test.want {
				t.Fatalf("healthPollInterval() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestPlanHealthConvergenceProjectsOnlyReachedHealthGates(t *testing.T) {
	t.Parallel()

	active := &domain.Healthcheck{Test: []string{testHealthCommand, testTrueCommand}}
	completed := domain.Hash([]byte("completed health-gated action"))
	base := Preparation{
		HasTransaction: true,
		Transaction: store.Transaction{
			ID: store.TransactionID{1}, Kind: store.TransactionBootstrap, State: store.TransactionActive,
		},
		Workload: domain.DesiredWorkload{Healthcheck: active},
		Plan: Plan{Observation: WorkloadObservation{
			State: WorkloadObservationPresent, Lifecycle: WorkloadLifecycleRunning,
			Health: WorkloadHealth{Status: WorkloadHealthHealthy},
		}},
	}
	if got := planHealthConvergence(base); got != HealthConvergenceNone {
		t.Fatalf("unreached gate = %q", got)
	}

	base.Actions = []store.Action{{
		Kind: workloadStartActionKind, State: store.ActionStateCompleted, PostconditionDigest: &completed,
	}}
	if got := planHealthConvergence(base); got != HealthConvergenceHealthy {
		t.Fatalf("healthy gate = %q", got)
	}
	base.Plan.Observation.Health = WorkloadHealth{Status: WorkloadHealthStarting}
	if got := planHealthConvergence(base); got != HealthConvergencePending {
		t.Fatalf("pending gate = %q", got)
	}
	base.Plan.Observation.Health = WorkloadHealth{Status: WorkloadHealthUnhealthy}
	if got := planHealthConvergence(base); got != HealthConvergenceDegraded {
		t.Fatalf("degraded gate = %q", got)
	}

	base.Plan.Kind = PlanRestore
	base.Actions[0].Kind = workloadRestoreStartActionKind
	base.Workload.Healthcheck = nil
	base.HasApplied = true
	base.Applied.Healthcheck = true
	base.Plan.Observation.Health = WorkloadHealth{Status: WorkloadHealthHealthy}
	if got := planHealthConvergence(base); got != HealthConvergenceHealthy {
		t.Fatalf("restore predecessor health contract = %q", got)
	}
}
