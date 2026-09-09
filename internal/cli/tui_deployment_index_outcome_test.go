package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeploymentIndexRenameErrorReprovesPublication(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	draft := newDeploymentDraftFixture(t)
	operations := defaultDeploymentGitFileOperations()
	rename := operations.rename
	operations.rename = func(before, after string) error {
		if err := rename(before, after); err != nil {
			return err
		}
		cancel()

		return errDeploymentCoverage
	}
	if err := stageConfirmedDeploymentWith(ctx, draft, replaceDeploymentEntry, operations); err != nil {
		t.Fatalf("proven successful rename: %v", err)
	}
	assertDeploymentIndexPublication(t, draft)
}

func TestDeploymentIndexPublicationRequiresLockIdentity(t *testing.T) {
	t.Parallel()
	draft := newDeploymentDraftFixture(t)
	operations := defaultDeploymentGitFileOperations()
	lstat := operations.lstat
	operations.lstat = func(path string) (os.FileInfo, error) {
		if filepath.Base(path) == "index.lock" {
			return nil, errDeploymentCoverage
		}

		return lstat(path)
	}
	err := stageConfirmedDeploymentWith(t.Context(), draft, replaceDeploymentEntry, operations)
	if !errors.Is(err, errDeploymentCoverage) || !errors.Is(err, errDeploymentPublishFailed) {
		t.Fatalf("unproven lock identity = %v", err)
	}
	assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
	state, err := cleanGitTree(t.Context(), draft.repository)
	if err != nil || state != draft.base {
		t.Fatalf("pre-publication failure changed repository: %+v, %v", state, err)
	}
	assertDeploymentIndexUnlocked(t, draft.repository)
}

func TestDeploymentIndexRenameErrorPreservesConcurrentWriter(t *testing.T) {
	t.Parallel()
	draft := newDeploymentDraftFixture(t)
	operations := defaultDeploymentGitFileOperations()
	rename := operations.rename
	operations.rename = func(before, after string) error {
		if err := rename(before, after); err != nil {
			return err
		}
		writeGitSourceTestFile(t, draft.repository, "concurrent", []byte("other writer\n"), 0o600)
		if _, err := runGit(t.Context(), draft.repository, "add", "--", "concurrent"); err != nil {
			t.Fatal(err)
		}
		writeGitSourceTestFile(t, filepath.Dir(before), filepath.Base(before), []byte("other writer lock"), 0o600)

		return errDeploymentCoverage
	}
	err := stageConfirmedDeploymentWith(t.Context(), draft, replaceDeploymentEntry, operations)
	if !errors.Is(err, errDeploymentWorktreeUnknown) {
		t.Fatalf("concurrent publication classified as settled: %v", err)
	}
	assertDeploymentIndexPublication(t, draft)
	other, err := runGit(t.Context(), draft.repository, "show", ":concurrent")
	if err != nil || string(other) != "other writer\n" {
		t.Fatalf("concurrent index lost: %q, %v", other, err)
	}
	lock, err := os.ReadFile(filepath.Join(draft.repository, ".git/index.lock"))
	if err != nil || string(lock) != "other writer lock" {
		t.Fatalf("concurrent lock lost: %q, %v", lock, err)
	}
	workspace := &tuiDeploymentWorkspace{draft: &draft}
	if _, err := workspace.Stage(t.Context()); err == nil {
		t.Fatal("Stage accepted unknown publication")
	}
	if _, err := workspace.Commit(t.Context(), "must not commit", false); err == nil {
		t.Fatal("Commit accepted unknown publication")
	}
	assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.candidate.Content)
}

func assertDeploymentIndexPublication(t *testing.T, draft tuiDeploymentDraft) {
	t.Helper()
	assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.candidate.Content)
	staged, err := runGit(t.Context(), draft.repository, "show", ":"+draft.entry)
	if err != nil || !bytes.Equal(staged, draft.confirmation.content) {
		t.Fatalf("published index = %q, %v", staged, err)
	}
	head, err := resolveGitObject(t.Context(), draft.repository, "HEAD^{commit}")
	if err != nil || head != draft.base.head {
		t.Fatalf("HEAD changed: %q, %v", head, err)
	}
}
