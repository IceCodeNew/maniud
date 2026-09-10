package cli

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	gitOpsCycleConverged              = "converged"
	gitOpsCyclePartial                = "partial"
	gitOpsCycleAwaitingPush           = "awaiting_push"
	gitOpsCycleFailed                 = "failed"
	gitOpsSkippedInvalidComposeSource = string(application.EventReasonInvalidComposeSource)
	shortGitCommitLength              = 12
)

type gitOpsSkippedSource struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

type gitOpsPreparedSnapshot struct {
	services []gitOpsServiceSnapshot
	skipped  []gitOpsSkippedSource
}

type gitOpsCycleCounts struct {
	applied                 int
	unchanged               int
	skipped                 int
	failed                  int
	deferred                int
	skippedSources          []gitOpsSkippedSource
	sourceBlockersObserved  bool
	recoveryBlockerObserved bool
}

type gitOpsCycleSummary struct {
	Commit         string                `json:"commit"`
	Status         string                `json:"status"`
	Applied        int                   `json:"applied"`
	Unchanged      int                   `json:"unchanged"`
	Skipped        int                   `json:"skipped"`
	Failed         int                   `json:"failed"`
	Deferred       int                   `json:"deferred"`
	SkippedSources []gitOpsSkippedSource `json:"skipped_sources,omitempty"`
}

type gitOpsSourceBlockerEvents struct {
	sink            application.EventSink
	activeSources   []gitOpsSkippedSource
	recoveryBlocked bool
}

func (events *gitOpsSourceBlockerEvents) TryPublish(event application.Event) bool {
	if events == nil {
		return false
	}
	if event.Kind == application.EventDaemonUnavailable && events.recoveryBlocked {
		return true
	}
	if events.sink == nil {
		return false
	}

	return events.sink.TryPublish(event)
}

func (events *gitOpsSourceBlockerEvents) observe(
	runErr error,
	counts gitOpsCycleCounts,
) {
	if events == nil {
		return
	}
	if counts.sourceBlockersObserved {
		events.observeSources(counts.skippedSources)
	}
	if counts.recoveryBlockerObserved {
		events.observeRecovery(errors.Is(runErr, errGitOpsRecoverySourceBlocked))
	}
}

func (events *gitOpsSourceBlockerEvents) observeSources(current []gitOpsSkippedSource) {
	activeSet := make(map[gitOpsSkippedSource]struct{}, len(events.activeSources))
	for _, blocker := range events.activeSources {
		activeSet[blocker] = struct{}{}
	}
	currentSet := make(map[gitOpsSkippedSource]struct{}, len(current))
	for _, blocker := range current {
		currentSet[blocker] = struct{}{}
		if _, active := activeSet[blocker]; !active {
			events.publish(application.EventGitOpsSourceBlocked, blocker)
		}
	}
	for _, blocker := range events.activeSources {
		if _, active := currentSet[blocker]; !active {
			events.publish(application.EventGitOpsSourceRecovered, blocker)
		}
	}
	events.activeSources = slices.Clone(current)
}

func (events *gitOpsSourceBlockerEvents) observeRecovery(blocked bool) {
	if blocked == events.recoveryBlocked {
		return
	}
	kind := application.EventGitOpsSourceRecovered
	if blocked {
		kind = application.EventGitOpsSourceBlocked
	}
	events.publish(kind, gitOpsRecoverySourceBlocker())
	events.recoveryBlocked = blocked
}

func (events *gitOpsSourceBlockerEvents) publish(
	kind application.EventKind,
	blocker gitOpsSkippedSource,
) {
	publishCLIEvent(events, application.Event{
		Kind: kind, Source: blocker.Path, Reason: application.EventReason(blocker.Code),
	})
}

func gitOpsRecoverySourceBlocker() gitOpsSkippedSource {
	return gitOpsSkippedSource{Code: errGitOpsRecoverySourceBlocked.Error()}
}

func (counts *gitOpsCycleCounts) add(other gitOpsCycleCounts) {
	counts.applied += other.applied
	counts.unchanged += other.unchanged
	counts.skipped += other.skipped
	counts.failed += other.failed
	counts.deferred += other.deferred
	counts.skippedSources = append(counts.skippedSources, other.skippedSources...)
	counts.sourceBlockersObserved = counts.sourceBlockersObserved || other.sourceBlockersObserved
	counts.recoveryBlockerObserved = counts.recoveryBlockerObserved || other.recoveryBlockerObserved
}

func (counts *gitOpsCycleCounts) markFailed() {
	if counts.failed == 0 {
		counts.failed = 1
	}
}

func skippedGitOpsSource(root, path string) gitOpsSkippedSource {
	// listGitOpsServiceFiles returns direct children under the same absolute root,
	// so filepath.Rel cannot fail on the supported Unix platforms.
	relative, _ := filepath.Rel(root, path)

	return gitOpsSkippedSource{
		Path: filepath.ToSlash(relative), Code: gitOpsSkippedInvalidComposeSource,
	}
}

func gitOpsCycleStatusFor(counts gitOpsCycleCounts, err error) string {
	switch {
	case errors.Is(err, errGitOpsRecoverySourceBlocked):
		return errGitOpsRecoverySourceBlocked.Error()
	case err != nil:
		return gitOpsCycleFailed
	case counts.skipped != 0:
		return gitOpsCyclePartial
	default:
		return gitOpsCycleConverged
	}
}

func writeGitOpsCycleSummary(
	output io.Writer,
	commit string,
	status string,
	counts gitOpsCycleCounts,
) error {
	if !validGitObjectID(commit) || !validGitOpsCycleStatus(status) ||
		!validGitOpsCycleCounts(counts) {
		return errGitOpsRepositoryInvalid
	}
	summary := gitOpsCycleSummary{
		Commit:  commit[:min(len(commit), shortGitCommitLength)],
		Status:  status,
		Applied: counts.applied, Unchanged: counts.unchanged, Skipped: counts.skipped,
		Failed: counts.failed, Deferred: counts.deferred,
		SkippedSources: counts.skippedSources,
	}
	// The summary contains only bounded JSON scalar and slice fields, so Marshal cannot fail.
	encoded, _ := json.Marshal(&summary)
	encoded = append(encoded, '\n')
	written, err := output.Write(encoded)
	if err != nil {
		return fmt.Errorf("write gitops cycle summary: %w", err)
	}
	if written != len(encoded) {
		return fmt.Errorf("write gitops cycle summary: %w", io.ErrShortWrite)
	}

	return nil
}

func finishGitOpsCycle(
	output io.Writer,
	commit string,
	status string,
	counts gitOpsCycleCounts,
	runErr error,
	events application.EventSink,
) error {
	summaryErr := writeGitOpsCycleSummary(output, commit, status, counts)
	if summaryErr == nil {
		if blockerEvents, valid := events.(*gitOpsSourceBlockerEvents); valid {
			blockerEvents.observe(runErr, counts)
		}
	}

	return errors.Join(runErr, summaryErr)
}

func validGitOpsCycleStatus(status string) bool {
	switch status {
	case gitOpsCycleConverged,
		gitOpsCyclePartial,
		gitOpsCycleAwaitingPush,
		gitOpsCycleFailed,
		errGitOpsRecoverySourceBlocked.Error():
		return true
	default:
		return false
	}
}

func validGitOpsCycleCounts(counts gitOpsCycleCounts) bool {
	return validGitOpsCycleTotals(counts) && validGitOpsSkippedSources(counts)
}

func validGitOpsCycleTotals(counts gitOpsCycleCounts) bool {
	return counts.applied >= 0 && counts.unchanged >= 0 && counts.skipped >= 0 &&
		counts.failed >= 0 && counts.failed <= 1 && counts.deferred >= 0 &&
		counts.skipped == len(counts.skippedSources)
}

func validGitOpsSkippedSources(counts gitOpsCycleCounts) bool {
	for _, source := range counts.skippedSources {
		path := filepath.FromSlash(source.Path)
		if source.Code != gitOpsSkippedInvalidComposeSource ||
			!filepath.IsLocal(path) || filepath.Dir(path) != gitOpsServicesDirectory ||
			filepath.ToSlash(filepath.Clean(path)) != source.Path {
			return false
		}
	}

	return true
}
