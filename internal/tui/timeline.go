package tui

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	maximumTimelineEntries        = 128
	maximumTimelineBytes          = 64 << 10
	transactionIdentityCharacters = 32
)

type observationOutcome string

const (
	observationCorrelated observationOutcome = "correlated"
	observationStale      observationOutcome = "stale"
	observationInvalid    observationOutcome = "invalid"
)

type eventCorrelation struct {
	sequence    uint64
	project     string
	service     string
	plan        application.PlanKind
	runtime     domain.RuntimeKind
	transaction string
	evidence    []string
}

type timelineEntry struct {
	sequence           uint64
	generation         uint64
	elapsed            time.Duration
	stage              string
	code               string
	attempt            int
	outcome            observationOutcome
	plan               application.PlanKind
	runtime            domain.RuntimeKind
	transaction        string
	source             string
	evidence           string
	operationSequence  int64
	postconditionMatch bool
}

type sessionTimeline struct {
	startedAt time.Time
	now       func() time.Time
	entries   []timelineEntry
	bytes     int
	sequence  uint64
	truncated bool
}

func newSessionTimeline() sessionTimeline {
	now := time.Now

	return sessionTimeline{startedAt: now(), now: now}
}

func (timeline *sessionTimeline) observe(
	event application.Event,
	operationSequence uint64,
	correlation eventCorrelation,
) {
	if timeline.truncated {
		return
	}

	timeline.sequence++
	entry := timelineEntryForEvent(timeline.sequence, timeline.elapsed(), event, operationSequence, correlation)
	line := entry.line()
	if len(timeline.entries) == maximumTimelineEntries || timeline.bytes+len(line)+1 > maximumTimelineBytes {
		timeline.truncated = true

		return
	}
	timeline.entries = append(timeline.entries, entry)
	timeline.bytes += len(line) + 1
}

func (timeline *sessionTimeline) elapsed() time.Duration {
	if timeline.now == nil || timeline.startedAt.IsZero() {
		return 0
	}
	elapsed := timeline.now().Sub(timeline.startedAt)
	if elapsed < 0 {
		return 0
	}

	return elapsed.Truncate(time.Millisecond)
}

func timelineEntryForEvent(
	sequence uint64,
	elapsed time.Duration,
	event application.Event,
	operationSequence uint64,
	correlation eventCorrelation,
) timelineEntry {
	entry := timelineEntry{
		sequence: sequence, generation: operationSequence,
		elapsed: elapsed, stage: string(event.Kind), code: string(event.Kind),
		attempt: 1, outcome: observationStale,
	}
	if !validEventKind(event.Kind) || !validEventPlan(event.Plan) || !validEventRuntime(event.Runtime) {
		entry.stage = "application_event"
		entry.code = "invalid_event"
		entry.outcome = observationInvalid

		return entry
	}
	entry.plan = event.Plan
	entry.runtime = event.Runtime
	if !projectEventIdentity(&entry, event) {
		entry.outcome = observationInvalid

		return entry
	}
	entry.operationSequence = event.Sequence
	entry.postconditionMatch = event.Satisfied
	if eventCorrelates(event, operationSequence, correlation) {
		entry.outcome = observationCorrelated
	}

	return entry
}

func projectEventIdentity(entry *timelineEntry, event application.Event) bool {
	if event.Reason != "" && !validEventReason(event.Reason) {
		return false
	}
	if event.Reason != "" {
		entry.code = string(event.Reason)
	}
	var valid bool
	entry.transaction, valid = opaqueTransaction(event.Transaction)
	if event.Transaction != "" && !valid {
		return false
	}
	entry.evidence, valid = opaqueDigest(event.Evidence)
	if event.Evidence != "" && !valid {
		return false
	}
	entry.source, _ = opaqueDigest(event.Source)

	return true
}

func (timeline *sessionTimeline) latestCorrelated(correlation eventCorrelation) string {
	for _, entry := range slices.Backward(timeline.entries) {
		if entry.outcome == observationCorrelated && entry.generation == correlation.sequence {
			return entry.stage
		}
	}

	return ""
}

func validEventKind(kind application.EventKind) bool {
	switch kind {
	case application.EventPlanPrepared,
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
		application.EventDaemonUnavailable:
		return true
	default:
		return false
	}
}

func validEventPlan(kind application.PlanKind) bool {
	switch kind {
	case "", application.PlanBootstrap, application.PlanAdopt, application.PlanUnchanged,
		application.PlanUpgrade, application.PlanResume, application.PlanProbeUnknownEffect,
		application.PlanRestore:
		return true
	default:
		return false
	}
}

func validEventRuntime(kind domain.RuntimeKind) bool {
	return kind == "" || kind.SupportsWorkloads()
}

func validEventReason(reason application.EventReason) bool {
	switch reason {
	case application.EventReasonInvalidComposeSource, application.EventReasonRecoverySourceBlocked:
		return true
	default:
		return false
	}
}

func opaqueTransaction(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	if len(value) != transactionIdentityCharacters ||
		value == strings.Repeat("0", transactionIdentityCharacters) || value != strings.ToLower(value) {
		return "", false
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", false
	}

	return value, true
}

func opaqueDigest(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	if _, err := domain.ParseDigest(value); err != nil {
		return "", false
	}

	return value, true
}

func eventCorrelates(
	event application.Event,
	operationSequence uint64,
	correlation eventCorrelation,
) bool {
	if correlation.sequence == 0 || operationSequence != correlation.sequence || correlation.project == "" ||
		event.Project != correlation.project ||
		event.Service != correlation.service || event.Runtime != correlation.runtime {
		return false
	}
	if event.Plan != "" && event.Plan != correlation.plan {
		return false
	}

	return eventIdentityCorrelates(event, correlation)
}

func eventIdentityCorrelates(event application.Event, correlation eventCorrelation) bool {
	if event.Transaction != "" && event.Transaction != correlation.transaction {
		return false
	}
	if event.Evidence != "" && !slices.Contains(correlation.evidence, event.Evidence) {
		return false
	}

	return true
}

func (entry timelineEntry) line() string {
	line := fmt.Sprintf(
		"#%d +%dms flow=application stage=%s code=%s attempt=%d outcome=%s",
		entry.sequence,
		entry.elapsed.Milliseconds(),
		entry.stage,
		entry.code,
		entry.attempt,
		entry.outcome,
	)
	if entry.plan != "" {
		line += " plan=" + string(entry.plan)
	}
	if entry.runtime != "" {
		line += " runtime=" + entry.runtime.String()
	}
	if entry.transaction != "" {
		line += " transaction=" + entry.transaction
	}
	if entry.source != "" {
		line += " source=" + entry.source
	}
	if entry.evidence != "" {
		line += " evidence=" + entry.evidence
	}
	if entry.operationSequence != 0 {
		line += fmt.Sprintf(" operation_sequence=%d", entry.operationSequence)
	}
	if entry.postconditionMatch {
		line += " satisfied=true"
	}

	return line
}

func eventCorrelationForSnapshot(sequence uint64, snapshot application.OperationSnapshot) eventCorrelation {
	correlation := eventCorrelation{
		sequence: sequence,
		project:  snapshot.Plan.Project, service: snapshot.Plan.Service,
		plan: snapshot.Plan.Kind, runtime: snapshot.Plan.Runtime,
	}
	if snapshot.HasTransaction {
		correlation.transaction = snapshot.Transaction.ID
	}
	for _, action := range snapshot.Actions {
		if action.Postcondition != "" {
			correlation.evidence = append(correlation.evidence, action.Postcondition)
		}
	}

	return correlation
}

func (state *model) observeApplicationEvent(observation eventMsg) {
	correlation := eventCorrelation{}
	switch current := state.page.(type) {
	case reviewPage:
		correlation = current.correlation
	case detailsPage:
		correlation = current.review.correlation
	case confirmationPage:
		correlation = current.review.correlation
	}
	state.timeline.observe(observation.event, observation.sequence, correlation)
}

func exportableReview(current page) (reviewPage, bool) {
	switch current := current.(type) {
	case reviewPage:
		return current, true
	case detailsPage:
		return current.review, true
	default:
		return reviewPage{}, false
	}
}

type detailProjection struct {
	current   string
	proposed  string
	timeline  []string
	dropped   uint64
	truncated bool
}

func (state *model) detailProjection(review reviewPage) detailProjection {
	lines := make([]string, 0, len(state.timeline.entries))
	for _, entry := range state.timeline.entries {
		lines = append(lines, entry.line())
	}
	dropped := uint64(0)
	if state.events != nil {
		dropped = state.events.DroppedEvents()
	}

	return detailProjection{
		current: review.plan.current, proposed: review.plan.proposed, timeline: lines,
		dropped: dropped, truncated: state.timeline.truncated,
	}
}

func (projection detailProjection) plain() string {
	var result strings.Builder
	result.WriteString("Maniud session details\n\nCURRENT\n")
	result.WriteString(projection.current)
	result.WriteString("\n\nPROPOSED\n")
	result.WriteString(projection.proposed)
	result.WriteString("\n\nSESSION TIMELINE\n")
	if len(projection.timeline) == 0 {
		result.WriteString("No application observations.\n")
	} else {
		for _, line := range projection.timeline {
			result.WriteString(line)
			result.WriteByte('\n')
		}
	}
	fmt.Fprintf(&result, "Dropped events: %d\n", projection.dropped)
	if projection.truncated {
		result.WriteString("Timeline truncated: yes\n")
	}

	return result.String()
}
