package cli

import (
	"bytes"
	"errors"
	"io"
	"testing"
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

type shortGitOpsCycleWriter struct{}

func (shortGitOpsCycleWriter) Write(content []byte) (int, error) {
	return len(content) - 1, nil
}
