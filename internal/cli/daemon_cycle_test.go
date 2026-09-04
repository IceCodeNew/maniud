package cli

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
)

const gitOpsCycleTestCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestWriteGitOpsCycleSummaryUsesBoundedSafeFields(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	counts := gitOpsCycleCounts{
		applied: 1, unchanged: 2, skipped: 1, deferred: 3,
		skippedSources: []gitOpsSkippedSource{{
			Path: tuiTestServicePath, Code: gitOpsSkippedInvalidComposeSource,
		}},
	}
	if err := writeGitOpsCycleSummary(
		output, gitOpsCycleTestCommit, gitOpsCyclePartial, counts,
	); err != nil {
		t.Fatalf("writeGitOpsCycleSummary() error = %v", err)
	}
	want := "{\"commit\":\"aaaaaaaaaaaa\",\"status\":\"partial\",\"applied\":1," +
		"\"unchanged\":2,\"skipped\":1,\"failed\":0,\"deferred\":3," +
		"\"skipped_sources\":[{\"path\":\"services/api.yaml\"," +
		"\"code\":\"invalid_compose_source\"}]}\n"
	if output.String() != want {
		t.Fatalf("writeGitOpsCycleSummary() = %q, want %q", output.String(), want)
	}
}

func TestWriteGitOpsCycleSummaryRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		commit string
		status string
		counts gitOpsCycleCounts
	}{
		{name: "invalid commit", commit: testInvalidValue, status: gitOpsCycleConverged},
		{name: "status", commit: gitOpsCycleTestCommit, status: testInvalidValue},
		{
			name: "negative", commit: gitOpsCycleTestCommit, status: gitOpsCycleConverged,
			counts: gitOpsCycleCounts{applied: -1},
		},
		{
			name: "failed", commit: gitOpsCycleTestCommit, status: gitOpsCycleFailed,
			counts: gitOpsCycleCounts{failed: 2},
		},
		{
			name: "missing skipped source", commit: gitOpsCycleTestCommit, status: gitOpsCyclePartial,
			counts: gitOpsCycleCounts{skipped: 1},
		},
		{
			name: "path", commit: gitOpsCycleTestCommit, status: gitOpsCyclePartial,
			counts: gitOpsCycleCounts{skipped: 1, skippedSources: []gitOpsSkippedSource{{
				Path: "../compose.yaml", Code: gitOpsSkippedInvalidComposeSource,
			}}},
		},
		{
			name: "code", commit: gitOpsCycleTestCommit, status: gitOpsCyclePartial,
			counts: gitOpsCycleCounts{skipped: 1, skippedSources: []gitOpsSkippedSource{{
				Path: tuiTestServicePath, Code: testInvalidValue,
			}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := writeGitOpsCycleSummary(
				io.Discard, test.commit, test.status, test.counts,
			); !errors.Is(err, errGitOpsRepositoryInvalid) {
				t.Fatalf("writeGitOpsCycleSummary() error = %v", err)
			}
		})
	}
}

func TestWriteGitOpsCycleSummaryReportsWriterFailures(t *testing.T) {
	t.Parallel()

	if err := writeGitOpsCycleSummary(
		failingWriterWithError{err: errClosedOutput},
		gitOpsCycleTestCommit,
		gitOpsCycleConverged,
		gitOpsCycleCounts{},
	); !errors.Is(err, errClosedOutput) {
		t.Fatalf("writeGitOpsCycleSummary(writer failure) error = %v", err)
	}
	if err := writeGitOpsCycleSummary(
		shortGitOpsCycleWriter{},
		gitOpsCycleTestCommit,
		gitOpsCycleConverged,
		gitOpsCycleCounts{},
	); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeGitOpsCycleSummary(short write) error = %v", err)
	}
}

func TestGitOpsCycleStatuses(t *testing.T) {
	t.Parallel()

	for _, status := range []string{
		gitOpsCycleConverged,
		gitOpsCyclePartial,
		gitOpsCycleAwaitingPush,
		gitOpsCycleFailed,
		errGitOpsRecoverySourceBlocked.Error(),
	} {
		if !validGitOpsCycleStatus(status) {
			t.Fatalf("validGitOpsCycleStatus(%q) = false", status)
		}
	}
	if validGitOpsCycleStatus(testInvalidValue) {
		t.Fatal("validGitOpsCycleStatus(invalid) = true")
	}

	if status := gitOpsCycleStatusFor(gitOpsCycleCounts{}, nil); status != gitOpsCycleConverged {
		t.Fatalf("gitOpsCycleStatusFor(converged) = %q", status)
	}
	if status := gitOpsCycleStatusFor(gitOpsCycleCounts{skipped: 1}, nil); status != gitOpsCyclePartial {
		t.Fatalf("gitOpsCycleStatusFor(partial) = %q", status)
	}
	if status := gitOpsCycleStatusFor(gitOpsCycleCounts{}, errApplyTest); status != gitOpsCycleFailed {
		t.Fatalf("gitOpsCycleStatusFor(failed) = %q", status)
	}
	if status := gitOpsCycleStatusFor(
		gitOpsCycleCounts{}, errGitOpsRecoverySourceBlocked,
	); status != errGitOpsRecoverySourceBlocked.Error() {
		t.Fatalf("gitOpsCycleStatusFor(blocked) = %q", status)
	}
}

func TestGitOpsCycleCountTransitions(t *testing.T) {
	t.Parallel()

	counts := gitOpsCycleCounts{applied: 1}
	counts.add(gitOpsCycleCounts{unchanged: 2, skipped: 1, deferred: 3})
	counts.markFailed()
	counts.markFailed()
	if counts.applied != 1 || counts.unchanged != 2 || counts.skipped != 1 ||
		counts.failed != 1 || counts.deferred != 3 {
		t.Fatalf("gitOpsCycleCounts transitions = %#v", counts)
	}
}

func TestGitOpsSourceBlockerEventsPublishOnlyTransitions(t *testing.T) {
	t.Parallel()

	var observed []application.Event
	events := &gitOpsSourceBlockerEvents{sink: cliEventSinkFunc(func(event application.Event) bool {
		observed = append(observed, event)

		return true
	})}
	api := gitOpsSkippedSource{Path: "services/api.yaml", Code: gitOpsSkippedInvalidComposeSource}
	worker := gitOpsSkippedSource{Path: "services/worker.yml", Code: gitOpsSkippedInvalidComposeSource}
	web := gitOpsSkippedSource{Path: "services/web.yaml", Code: gitOpsSkippedInvalidComposeSource}

	events.observe(nil, observedGitOpsSources(api, worker))
	events.observe(nil, observedGitOpsSources(api, worker))
	events.observe(nil, observedGitOpsSources(worker, web))
	events.observe(nil, gitOpsCycleCounts{sourceBlockersObserved: true})

	want := []application.Event{
		gitOpsSourceBlockerEvent(application.EventGitOpsSourceBlocked, api),
		gitOpsSourceBlockerEvent(application.EventGitOpsSourceBlocked, worker),
		gitOpsSourceBlockerEvent(application.EventGitOpsSourceBlocked, web),
		gitOpsSourceBlockerEvent(application.EventGitOpsSourceRecovered, api),
		gitOpsSourceBlockerEvent(application.EventGitOpsSourceRecovered, worker),
		gitOpsSourceBlockerEvent(application.EventGitOpsSourceRecovered, web),
	}
	if !slices.Equal(observed, want) {
		t.Fatalf("source blocker transitions = %#v, want %#v", observed, want)
	}

	restarted := &gitOpsSourceBlockerEvents{sink: events.sink}
	restarted.observe(nil, observedGitOpsSources(api))
	if len(observed) != len(want)+1 || observed[len(want)] != want[0] {
		t.Fatalf("source blocker restart transition = %#v", observed)
	}
}

func TestGitOpsSourceBlockerEventsSuppressRecoveryUnavailableDuplicates(t *testing.T) {
	t.Parallel()

	var observed []application.Event
	events := &gitOpsSourceBlockerEvents{sink: cliEventSinkFunc(func(event application.Event) bool {
		observed = append(observed, event)

		return true
	})}
	recovery := gitOpsRecoverySourceBlocker()
	events.observe(errors.Join(errApplyTest, errGitOpsRecoverySourceBlocked), gitOpsCycleCounts{
		recoveryBlockerObserved: true,
	})
	if !events.TryPublish(application.Event{Kind: application.EventDaemonUnavailable}) {
		t.Fatal("recovery duplicate was not suppressed")
	}
	events.observe(errGitOpsRecoverySourceBlocked, gitOpsCycleCounts{
		recoveryBlockerObserved: true,
	})
	events.observe(errApplyTest, gitOpsCycleCounts{})
	if !events.TryPublish(application.Event{Kind: application.EventDaemonUnavailable}) {
		t.Fatal("unobserved recovery state was cleared")
	}
	events.observe(nil, gitOpsCycleCounts{recoveryBlockerObserved: true})
	if !events.TryPublish(application.Event{Kind: application.EventDaemonUnavailable}) {
		t.Fatal("post-recovery daemon event was dropped")
	}

	want := []application.Event{
		gitOpsSourceBlockerEvent(application.EventGitOpsSourceBlocked, recovery),
		gitOpsSourceBlockerEvent(application.EventGitOpsSourceRecovered, recovery),
		{Kind: application.EventDaemonUnavailable},
	}
	if !slices.Equal(observed, want) {
		t.Fatalf("recovery blocker transitions = %#v, want %#v", observed, want)
	}
}

func TestGitOpsSourceBlockerEventsContainNilDestinations(t *testing.T) {
	t.Parallel()

	var missing *gitOpsSourceBlockerEvents
	if missing.TryPublish(application.Event{}) {
		t.Fatal("nil blocker events accepted an event")
	}
	missing.observe(nil, gitOpsCycleCounts{})

	events := &gitOpsSourceBlockerEvents{}
	if events.TryPublish(application.Event{}) {
		t.Fatal("blocker events without sink accepted an event")
	}
}

func TestFinishGitOpsCycleRejectsNotificationEvidenceWithInvalidSummary(t *testing.T) {
	t.Parallel()

	var observed []application.Event
	events := &gitOpsSourceBlockerEvents{sink: cliEventSinkFunc(func(event application.Event) bool {
		observed = append(observed, event)

		return true
	})}
	err := finishGitOpsCycle(
		io.Discard,
		gitOpsCycleTestCommit,
		gitOpsCyclePartial,
		gitOpsCycleCounts{skipped: 1},
		nil,
		events,
	)
	if !errors.Is(err, errGitOpsRepositoryInvalid) || len(observed) != 0 {
		t.Fatalf("finishGitOpsCycle(invalid summary) = %v, events %#v", err, observed)
	}
}

func TestFinishGitOpsCyclePublishesValidatedSourceTransition(t *testing.T) {
	t.Parallel()

	var observed []application.Event
	events := &gitOpsSourceBlockerEvents{sink: cliEventSinkFunc(func(event application.Event) bool {
		observed = append(observed, event)

		return true
	})}
	blocker := gitOpsSkippedSource{
		Path: "services/api.yaml", Code: gitOpsSkippedInvalidComposeSource,
	}
	err := finishGitOpsCycle(
		io.Discard,
		gitOpsCycleTestCommit,
		gitOpsCyclePartial,
		observedGitOpsSources(blocker),
		nil,
		events,
	)
	want := gitOpsSourceBlockerEvent(application.EventGitOpsSourceBlocked, blocker)
	if err != nil || len(observed) != 1 || observed[0] != want {
		t.Fatalf("finishGitOpsCycle(valid summary) = %v, events %#v", err, observed)
	}
}

func gitOpsSourceBlockerEvent(
	kind application.EventKind,
	blocker gitOpsSkippedSource,
) application.Event {
	return application.Event{
		Kind: kind, Source: blocker.Path, Reason: application.EventReason(blocker.Code),
	}
}

func observedGitOpsSources(sources ...gitOpsSkippedSource) gitOpsCycleCounts {
	return gitOpsCycleCounts{
		skipped: len(sources), skippedSources: sources, sourceBlockersObserved: true,
	}
}

type shortGitOpsCycleWriter struct{}

func (shortGitOpsCycleWriter) Write(content []byte) (int, error) {
	return len(content) - 1, nil
}
