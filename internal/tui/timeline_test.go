package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

//nolint:cyclop // One projection contract checks independent inclusion and exclusion clauses.
func TestSessionTimelineProjectsCorrelatedStaleAndInvalidEvents(t *testing.T) {
	t.Parallel()

	started := time.Unix(100, 0)
	digest := domain.Hash([]byte("evidence")).String()
	transaction := strings.Repeat("a", 32)
	timeline := sessionTimeline{startedAt: started, now: func() time.Time {
		return started.Add(1250 * time.Millisecond)
	}}
	correlation := eventCorrelation{
		sequence: 7,
		project:  testProject, service: testService, plan: application.PlanUpgrade,
		runtime: domain.RuntimeDocker, transaction: transaction, evidence: []string{digest},
	}
	timeline.observe(application.Event{
		Kind: application.EventPostconditionObserved, Plan: application.PlanUpgrade,
		Project: testProject, Service: testService, Runtime: domain.RuntimeDocker,
		Transaction: transaction, Source: digest, Evidence: digest, Sequence: 3, Satisfied: true,
		Reason: application.EventReasonInvalidComposeSource,
	}, 7, correlation)
	timeline.observe(application.Event{
		Kind: application.EventPlanPrepared, Plan: application.PlanUpgrade,
		Project: testOtherValue, Service: "BARK_DEVICE_KEY=secret", Runtime: domain.RuntimeDocker,
		Source: "/private/user/compose.yaml",
	}, 7, correlation)
	timeline.observe(application.Event{
		Kind: "vendor_event", Project: "BARK_DEVICE_KEY=" + testSecretValue, Evidence: testSecretValue,
	}, 7, correlation)

	if len(timeline.entries) != 3 || timeline.entries[0].outcome != observationCorrelated ||
		timeline.entries[1].outcome != observationStale || timeline.entries[2].outcome != observationInvalid ||
		timeline.latestCorrelated(correlation) != string(application.EventPostconditionObserved) ||
		timeline.latestCorrelated(eventCorrelation{sequence: 8}) != "" {
		t.Fatalf("timeline = %#v", timeline)
	}
	line := timeline.entries[0].line()
	for _, value := range []string{
		"#1 +1250ms", "stage=postcondition_observed", "code=invalid_compose_source",
		"attempt=1 outcome=correlated", "plan=upgrade", "runtime=docker",
		"transaction=" + transaction, "source=" + digest, "evidence=" + digest,
		"operation_sequence=3", "satisfied=true",
	} {
		if !strings.Contains(line, value) {
			t.Fatalf("timeline line misses %q: %q", value, line)
		}
	}
	projection := detailProjection{
		current: testOldProjection, proposed: testNewProjection,
		timeline: []string{line, timeline.entries[1].line(), timeline.entries[2].line()},
		dropped:  2, truncated: true,
	}.plain()
	if !strings.Contains(projection, "Dropped events: 2\nTimeline truncated: yes\n") ||
		strings.Contains(projection, testSecretValue) || strings.Contains(projection, "/private/user") {
		t.Fatalf("plain projection = %q", projection)
	}
}

func TestSessionTimelineStopsAtEntryAndByteBounds(t *testing.T) {
	t.Parallel()

	event := application.Event{Kind: application.EventDaemonUnavailable}
	timeline := newSessionTimeline()
	for range maximumTimelineEntries {
		timeline.observe(event, 0, eventCorrelation{})
	}
	timeline.observe(event, 0, eventCorrelation{})
	if len(timeline.entries) != maximumTimelineEntries || !timeline.truncated ||
		timeline.sequence != maximumTimelineEntries+1 {
		t.Fatalf("entry-bounded timeline = %#v", timeline)
	}
	timeline.observe(event, 0, eventCorrelation{})
	if timeline.sequence != maximumTimelineEntries+1 {
		t.Fatal("truncated timeline accepted another event")
	}

	byteBounded := newSessionTimeline()
	byteBounded.bytes = maximumTimelineBytes
	byteBounded.observe(event, 0, eventCorrelation{})
	if !byteBounded.truncated || len(byteBounded.entries) != 0 {
		t.Fatalf("byte-bounded timeline = %#v", byteBounded)
	}
}

//nolint:cyclop,funlen // The table covers every closed event kind and correlation mismatch.
func TestTimelineValidationAndCorrelationBoundaries(t *testing.T) {
	t.Parallel()

	for _, kind := range []application.EventKind{
		application.EventPlanPrepared,
		application.EventActionIntentRecorded,
		application.EventRuntimeEffectStarted,
		application.EventPostconditionObserved,
		application.EventActionCompleted,
		application.EventTransactionSucceeded,
		application.EventTransactionDegraded,
		application.EventTransactionRestored,
		application.EventTransactionFailed,
		application.EventGitOpsServiceApplyFailed,
		application.EventGitOpsSourceBlocked,
		application.EventGitOpsSourceRecovered,
		application.EventDaemonUnavailable,
	} {
		if !validEventKind(kind) {
			t.Fatalf("validEventKind(%q) = false", kind)
		}
	}
	if validEventKind(testUnknownValue) {
		t.Fatal("validEventKind(unknown) = true")
	}
	for _, kind := range []application.PlanKind{
		"", application.PlanBootstrap, application.PlanAdopt, application.PlanUnchanged,
		application.PlanUpgrade, application.PlanResume, application.PlanProbeUnknownEffect,
		application.PlanRestore,
	} {
		if !validEventPlan(kind) {
			t.Fatalf("validEventPlan(%q) = false", kind)
		}
	}
	if validEventPlan(testUnknownValue) || !validEventRuntime("") || !validEventRuntime(domain.RuntimePodman) ||
		validEventRuntime(testUnknownValue) {
		t.Fatal("event plan or runtime validation drifted")
	}
	if !validEventReason(application.EventReasonInvalidComposeSource) ||
		!validEventReason(application.EventReasonRecoverySourceBlocked) || validEventReason("") {
		t.Fatal("event reason validation drifted")
	}

	transaction := strings.Repeat("a", 32)
	if got, valid := opaqueTransaction(""); got != "" || !valid {
		t.Fatalf("opaqueTransaction(empty) = %q, %t", got, valid)
	}
	if got, valid := opaqueTransaction(transaction); got != transaction || !valid {
		t.Fatalf("opaqueTransaction(valid) = %q, %t", got, valid)
	}
	for _, invalid := range []string{
		"a", strings.ToUpper(transaction), strings.Repeat("z", 32), strings.Repeat("0", 32),
	} {
		if _, valid := opaqueTransaction(invalid); valid {
			t.Fatalf("opaqueTransaction(%q) accepted", invalid)
		}
	}
	digest := domain.Hash([]byte("identity")).String()
	if got, valid := opaqueDigest(""); got != "" || !valid {
		t.Fatalf("opaqueDigest(empty) = %q, %t", got, valid)
	}
	if got, valid := opaqueDigest(digest); got != digest || !valid {
		t.Fatalf("opaqueDigest(valid) = %q, %t", got, valid)
	}
	if _, valid := opaqueDigest(testSecretValue); valid {
		t.Fatal("opaqueDigest(secret) accepted")
	}

	correlation := eventCorrelation{
		sequence: 7,
		project:  testProject, service: testService, plan: application.PlanUpgrade,
		runtime: domain.RuntimeDocker, transaction: transaction, evidence: []string{digest},
	}
	event := application.Event{
		Kind: application.EventActionCompleted, Plan: application.PlanUpgrade,
		Project: testProject, Service: testService, Runtime: domain.RuntimeDocker,
		Transaction: transaction, Source: digest, Evidence: digest,
	}
	if !eventCorrelates(event, 7, correlation) {
		t.Fatal("exact event did not correlate")
	}
	if eventCorrelates(event, 6, correlation) {
		t.Fatal("event from another operation correlated")
	}
	for _, change := range []func(*application.Event){
		func(event *application.Event) { event.Project = testOtherValue },
		func(event *application.Event) { event.Service = testOtherValue },
		func(event *application.Event) { event.Runtime = domain.RuntimePodman },
		func(event *application.Event) { event.Plan = application.PlanRestore },
		func(event *application.Event) { event.Transaction = strings.Repeat("b", 32) },
		func(event *application.Event) { event.Evidence = domain.Hash([]byte(testOtherValue)).String() },
	} {
		candidate := event
		change(&candidate)
		if eventCorrelates(candidate, 7, correlation) {
			t.Fatalf("mismatched event correlated: %#v", candidate)
		}
	}
}

func TestTimelineContainsInvalidIdentityAndClockStates(t *testing.T) {
	t.Parallel()

	correlation := eventCorrelation{project: testProject, service: testService, runtime: domain.RuntimeDocker}
	for _, event := range []application.Event{
		{Kind: application.EventPlanPrepared, Plan: application.PlanKind(testUnknownValue)},
		{Kind: application.EventPlanPrepared, Runtime: domain.RuntimeKind(testUnknownValue)},
		{Kind: application.EventPlanPrepared, Reason: application.EventReason(testUnknownValue)},
		{Kind: application.EventPlanPrepared, Transaction: testSecretValue},
		{Kind: application.EventPlanPrepared, Evidence: testSecretValue},
	} {
		entry := timelineEntryForEvent(1, 0, event, 0, correlation)
		if entry.outcome != observationInvalid {
			t.Fatalf("invalid event entry = %#v", entry)
		}
	}

	timeline := sessionTimeline{}
	if timeline.elapsed() != 0 {
		t.Fatal("zero timeline clock returned elapsed time")
	}
	timeline.startedAt = time.Unix(2, 0)
	timeline.now = func() time.Time { return time.Unix(1, 0) }
	if timeline.elapsed() != 0 {
		t.Fatal("backward clock returned elapsed time")
	}
}

//nolint:cyclop // Snapshot, stream, and page projections share one correlation fixture.
func TestEventCorrelationAndDetailProjectionUseSnapshotAndStream(t *testing.T) {
	t.Parallel()

	transaction := strings.Repeat("a", 32)
	evidence := domain.Hash([]byte("postcondition")).String()
	snapshot := application.OperationSnapshot{
		Plan: application.Plan{
			Kind: application.PlanUpgrade, Project: testProject, Service: testService,
			Runtime: domain.RuntimeDocker,
		},
		HasTransaction: true,
		Transaction:    application.SnapshotTransaction{ID: transaction},
		Actions:        []application.SnapshotAction{{}, {Postcondition: evidence}},
	}
	correlation := eventCorrelationForSnapshot(7, snapshot)
	if correlation.sequence != 7 || correlation.project != testProject || correlation.service != testService ||
		correlation.plan != application.PlanUpgrade || correlation.runtime != domain.RuntimeDocker ||
		correlation.transaction != transaction || !slices.Equal(correlation.evidence, []string{evidence}) {
		t.Fatalf("snapshot correlation = %#v", correlation)
	}
	if empty := eventCorrelationForSnapshot(0, application.OperationSnapshot{}); empty.sequence != 0 ||
		empty.project != "" {
		t.Fatalf("empty snapshot correlation = %#v", empty)
	}

	state := &model{events: NewEventStream(), timeline: newSessionTimeline()}
	for range eventQueueCapacity {
		if !state.events.TryPublish(application.Event{}) {
			t.Fatal("event stream filled early")
		}
	}
	_ = state.events.TryPublish(application.Event{})
	review := reviewPage{plan: planView{current: testOldProjection, proposed: testNewProjection}}
	projection := state.detailProjection(review)
	if projection.current != testOldProjection || projection.proposed != testNewProjection || projection.dropped != 1 ||
		projection.truncated || !strings.Contains(projection.plain(), "No application observations.") {
		t.Fatalf("detail projection = %#v / %q", projection, projection.plain())
	}
	state.events = nil
	if projection = state.detailProjection(review); projection.dropped != 0 {
		t.Fatalf("nil-stream detail projection = %#v", projection)
	}
	if _, valid := exportableReview(homePage{}); valid {
		t.Fatal("home page is exportable")
	}
	if got, valid := exportableReview(review); !valid || got.plan.current != testOldProjection {
		t.Fatalf("review export = %#v, %t", got, valid)
	}
	if got, valid := exportableReview(detailsPage{review: review}); !valid ||
		got.plan.proposed != testNewProjection {
		t.Fatalf("details export = %#v, %t", got, valid)
	}
}
