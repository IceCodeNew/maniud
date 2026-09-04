package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

//nolint:cyclop,funlen,gocyclo // The test keeps the edit, commit, history, and restore proof sequence contiguous.
func TestTUIDeploymentWorkspaceCommitsAndRestoresTrackedEdit(t *testing.T) {
	t.Parallel()

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	initial, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve initial HEAD error = %v", err)
	}
	fields, err := workspace.Fields(t.Context(), request)
	if err != nil || len(fields) != len(application.DeploymentFields()) ||
		fields[0].ID != application.DeploymentCPUs.ID() || fields[0].Value != "1" || !fields[0].Present {
		t.Fatalf("Fields() = %#v, %v", fields, err)
	}
	preview, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
	)
	if err != nil || preview.ComposePath != deploymentComposeEntry ||
		len(preview.Changes) != 1 || preview.Changes[0].FieldID != application.DeploymentCPUs.ID() ||
		preview.Changes[0].CurrentValue != "1" || preview.Changes[0].ProposedValue != testDeploymentCPUValue ||
		preview.Diff == "" {
		t.Fatalf("Preview() = %#v, %v", preview, err)
	}
	assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
	if _, err = cleanGitTree(t.Context(), repository); err != nil {
		t.Fatalf("Preview() changed repository: %v", err)
	}

	staged, err := workspace.Stage(t.Context())
	if err != nil || staged.ComposePath != deploymentComposeEntry || staged.Diff != preview.Diff ||
		!strings.Contains(staged.Diff, "+    cpus: 2.5") {
		t.Fatalf("Stage() = %#v, %v", staged, err)
	}
	result, err := workspace.Commit(t.Context(), staged.CommitMessage, true)
	if err != nil || result.Outcome != tui.CommitSucceeded {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	project, err := compose.Load(t.Context(), result.Request.Source)
	if err != nil {
		t.Fatalf("Load(committed request) error = %v", err)
	}
	spec, err := project.ServiceSpec("api")
	if err != nil || spec.CPUs != testDeploymentCPUValue {
		t.Fatalf("committed CPUs = %q, %v", spec.CPUs, err)
	}

	history, err := workspace.History(t.Context(), result.Request)
	if err != nil || len(history) != 2 || history[0].Subject != staged.CommitMessage ||
		history[0].SignaturePresent {
		t.Fatalf("History() = %#v, %v", history, err)
	}
	restore, err := workspace.PreviewRestore(t.Context(), result.Request, initial)
	if err != nil || restore.Restore != initial || restore.ComposePath != deploymentComposeEntry ||
		len(restore.Changes) != 1 || restore.Changes[0].FieldID != application.DeploymentCPUs.ID() ||
		restore.Changes[0].CurrentValue != testDeploymentCPUValue || restore.Changes[0].ProposedValue != "1" {
		t.Fatalf("PreviewRestore() = %#v, %v", restore, err)
	}
	restoredStage, err := workspace.Stage(t.Context())
	if err != nil || !strings.Contains(restoredStage.Diff, "+    cpus: 1") {
		t.Fatalf("Stage(restore) = %#v, %v", restoredStage, err)
	}
	restored, err := workspace.Commit(t.Context(), restoredStage.CommitMessage, true)
	if err != nil || restored.Outcome != tui.CommitSucceeded {
		t.Fatalf("Commit(restore) = %#v, %v", restored, err)
	}
	project, err = compose.Load(t.Context(), restored.Request.Source)
	if err != nil {
		t.Fatalf("Load(restored request) error = %v", err)
	}
	spec, err = project.ServiceSpec("api")
	if err != nil || spec.CPUs != "1" {
		t.Fatalf("restored CPUs = %q, %v", spec.CPUs, err)
	}
}

//nolint:cyclop,funlen,gocognit // Built-in transforms and drift boundaries share one repository fixture.
func TestTUIDeploymentWorkspaceFreezesGitAttributes(t *testing.T) {
	t.Parallel()

	t.Run("built-in normalization", func(t *testing.T) {
		t.Parallel()

		workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
		commitDeploymentAttributes(t, repository, deploymentComposeEntry+" text eol=lf\n")
		if _, err := workspace.Preview(
			t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
		); err != nil {
			t.Fatalf("Preview() error = %v", err)
		}
		if _, err := workspace.Stage(t.Context()); err != nil {
			t.Fatalf("Stage() error = %v", err)
		}
		if err := workspace.Discard(t.Context()); err != nil {
			t.Fatalf("Discard() error = %v", err)
		}
	})

	t.Run("external filter attribute", func(t *testing.T) {
		t.Parallel()

		workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
		commitDeploymentAttributes(t, repository, deploymentComposeEntry+" filter=hostile\n")
		if _, err := workspace.Preview(
			t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
		); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("Preview(external filter attribute) error = %v", err)
		}
		assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
	})

	t.Run("info attributes identity drift", func(t *testing.T) {
		t.Parallel()

		workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
		path, err := absoluteGitPath(t.Context(), repository, "info/attributes")
		if err != nil {
			t.Fatalf("absoluteGitPath(info/attributes) error = %v", err)
		}
		content := []byte(deploymentComposeEntry + " text\n")
		if err = os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("WriteFile(info/attributes) error = %v", err)
		}
		if _, err = workspace.Preview(
			t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
		); err != nil {
			t.Fatalf("Preview() error = %v", err)
		}
		replacement := path + ".replacement"
		if err = os.WriteFile(replacement, content, 0o600); err != nil {
			t.Fatalf("WriteFile(replacement) error = %v", err)
		}
		if err = os.Rename(replacement, path); err != nil {
			t.Fatalf("Rename(replacement) error = %v", err)
		}
		if _, err = workspace.Stage(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("Stage(attribute identity drift) error = %v", err)
		}
		assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
		if _, err = cleanGitTree(t.Context(), repository); err != nil {
			t.Fatalf("attribute identity drift changed repository: %v", err)
		}
	})

	for _, test := range []struct {
		name  string
		drift func(t *testing.T, repository string)
	}{
		{
			name: "info attributes drift",
			drift: func(t *testing.T, repository string) {
				t.Helper()

				path, err := absoluteGitPath(t.Context(), repository, "info/attributes")
				if err != nil {
					t.Fatalf("absoluteGitPath(info/attributes) error = %v", err)
				}
				if err = os.WriteFile(path, []byte("unrelated text\n"), 0o600); err != nil {
					t.Fatalf("WriteFile(info/attributes) error = %v", err)
				}
			},
		},
		{
			name: "built-in configuration drift",
			drift: func(t *testing.T, repository string) {
				t.Helper()

				if _, err := runGit(
					t.Context(), repository, "config", "--local", "core.autocrlf", "input",
				); err != nil {
					t.Fatalf("git config core.autocrlf error = %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
			if _, err := workspace.Preview(
				t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
			); err != nil {
				t.Fatalf("Preview() error = %v", err)
			}
			test.drift(t, repository)
			if _, err := workspace.Stage(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
				t.Fatalf("Stage(attribute drift) error = %v", err)
			}
			assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
			if _, err := cleanGitTree(t.Context(), repository); err != nil {
				t.Fatalf("attribute drift changed repository: %v", err)
			}
		})
	}
}

func commitDeploymentAttributes(t *testing.T, repository, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(repository, ".gitattributes"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(.gitattributes) error = %v", err)
	}
	if _, err := runGit(t.Context(), repository, "add", "--", ".gitattributes"); err != nil {
		t.Fatalf("git add .gitattributes error = %v", err)
	}
	commitDeploymentIndex(t, repository, "Add deployment attributes")
}

func TestTUIDeploymentWorkspaceDiscardsExactStagedEdit(t *testing.T) {
	t.Parallel()

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := workspace.Preview(
		t.Context(), request, application.DeploymentRestart.ID(), "unless-stopped", false,
	); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard() error = %v", err)
	}
	assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
	if _, err := cleanGitTree(t.Context(), repository); err != nil {
		t.Fatalf("Discard() repository state error = %v", err)
	}
	restaged, err := workspace.Stage(t.Context())
	if err != nil || restaged.Diff == "" {
		t.Fatalf("Stage(after discard) = %#v, %v", restaged, err)
	}
	if err = workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard(restaged edit) error = %v", err)
	}
}

//nolint:cyclop,funlen,gocognit // The subtests prove distinct external Git ownership outcomes.
func TestDeploymentRollbackPreservesConcurrentGitState(t *testing.T) {
	t.Parallel()

	t.Run("unrelated index entry", func(t *testing.T) {
		t.Parallel()

		workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
		if _, err := workspace.Preview(
			t.Context(), request, application.DeploymentRestart.ID(), "unless-stopped", false,
		); err != nil {
			t.Fatalf("Preview() error = %v", err)
		}
		if _, err := workspace.Stage(t.Context()); err != nil {
			t.Fatalf("Stage() error = %v", err)
		}
		externalPath := "external.txt"
		externalContent := []byte("external staged content\n")
		if err := os.WriteFile(filepath.Join(repository, externalPath), externalContent, 0o600); err != nil {
			t.Fatalf("WriteFile(external) error = %v", err)
		}
		if _, err := runGit(t.Context(), repository, "add", "--", externalPath); err != nil {
			t.Fatalf("git add external error = %v", err)
		}
		if err := workspace.Discard(t.Context()); err != nil {
			t.Fatalf("Discard() error = %v", err)
		}

		assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
		stagedCompose, err := runGit(t.Context(), repository, "show", ":"+deploymentComposeEntry)
		if err != nil || !bytes.Equal(stagedCompose, request.Source.Content) {
			t.Fatalf("staged Compose content = %q, %v", stagedCompose, err)
		}
		stagedExternal, err := runGit(t.Context(), repository, "show", ":"+externalPath)
		if err != nil || !bytes.Equal(stagedExternal, externalContent) {
			t.Fatalf("staged external content = %q, %v", stagedExternal, err)
		}
		assertDeploymentIndexUnlocked(t, repository)
	})

	t.Run("index", func(t *testing.T) {
		t.Parallel()

		draft := newDeploymentDraftFixture(t)
		externalContent := []byte("external staged content\n")
		diff := func(ctx context.Context, repository string, _ []string) ([]byte, error) {
			object, err := runGitProcess(
				ctx, repository, false, externalContent, nil, "hash-object", "-w", "--stdin",
			)
			entry, found, readErr := readGitTreeEntry(ctx, repository, draft.base.tree, draft.entry)
			if err != nil || readErr != nil || !found {
				t.Fatalf("prepare concurrent index: object error = %v, entry = %#v, %t, %v", err, entry, found, readErr)
			}
			if _, err = runGit(
				ctx, repository, "update-index", "--cacheinfo", entry.mode,
				strings.TrimSpace(string(object)), draft.entry,
			); err != nil {
				t.Fatalf("update concurrent index error = %v", err)
			}

			return nil, errDeploymentEditInvalid
		}
		if _, _, err := stageDeploymentEditWith(
			t.Context(), draft, replaceDeploymentEntry, diff, writeGitTree,
		); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("stageDeploymentEditWith(concurrent index) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
		stagedContent, err := runGit(t.Context(), draft.repository, "show", ":"+draft.entry)
		if err != nil || !bytes.Equal(stagedContent, externalContent) {
			t.Fatalf("concurrent index content = %q, %v", stagedContent, err)
		}
		assertDeploymentIndexUnlocked(t, draft.repository)
	})

	t.Run("head", func(t *testing.T) {
		t.Parallel()

		draft := newDeploymentDraftFixture(t)
		var concurrentHead string
		diff := func(ctx context.Context, repository string, _ []string) ([]byte, error) {
			if _, err := runGit(
				ctx, repository,
				"-c", "user.name=Maniud Tests", "-c", "user.email=maniud@example.invalid",
				"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Concurrent deployment commit",
			); err != nil {
				t.Fatalf("git concurrent commit error = %v", err)
			}
			var err error
			concurrentHead, err = resolveGitObject(ctx, repository, "HEAD^{commit}")
			if err != nil {
				t.Fatalf("resolve concurrent HEAD error = %v", err)
			}

			return nil, errDeploymentEditInvalid
		}
		if _, _, err := stageDeploymentEditWith(
			t.Context(), draft, replaceDeploymentEntry, diff, writeGitTree,
		); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("stageDeploymentEditWith(concurrent HEAD) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.candidate.Content)
		if concurrentHead == draft.base.head {
			t.Fatal("concurrent commit did not advance HEAD")
		}
		if _, err := cleanGitTree(t.Context(), draft.repository); err != nil {
			t.Fatalf("concurrent commit was changed by rollback: %v", err)
		}
		assertDeploymentIndexUnlocked(t, draft.repository)
	})
}

//nolint:cyclop,funlen,gocognit // Each case proves one index-lock rollback boundary and its compensation.
func TestDeploymentRollbackFailureBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("missing target entry", func(t *testing.T) {
		t.Parallel()

		draft := stagedDeploymentDraftFixture(t)
		draft.entry = testDeploymentMissingEntry
		if err := rollbackDeploymentEdit(t.Context(), draft); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("rollbackDeploymentEdit(missing target entry) error = %v", err)
		}
		assertDeploymentIndexUnlocked(t, draft.repository)
	})

	t.Run("existing index lock", func(t *testing.T) {
		t.Parallel()

		draft := stagedDeploymentDraftFixture(t)
		indexPath, err := absoluteGitPath(t.Context(), draft.repository, "index")
		if err != nil {
			t.Fatalf("absoluteGitPath(index) error = %v", err)
		}
		if err = os.WriteFile(indexPath+".lock", nil, 0o600); err != nil {
			t.Fatalf("WriteFile(index.lock) error = %v", err)
		}
		if err = rollbackDeploymentEdit(t.Context(), draft); err == nil {
			t.Fatal("rollbackDeploymentEdit(existing index lock) succeeded")
		}
		if err = os.Remove(indexPath + ".lock"); err != nil {
			t.Fatalf("Remove(index.lock) error = %v", err)
		}
	})

	t.Run("invalid index", func(t *testing.T) {
		t.Parallel()

		draft := stagedDeploymentDraftFixture(t)
		indexPath, err := absoluteGitPath(t.Context(), draft.repository, "index")
		if err != nil {
			t.Fatalf("absoluteGitPath(index) error = %v", err)
		}
		if err = os.WriteFile(indexPath, []byte("invalid index"), 0o600); err != nil {
			t.Fatalf("WriteFile(index) error = %v", err)
		}
		if err = rollbackDeploymentEdit(t.Context(), draft); err == nil {
			t.Fatal("rollbackDeploymentEdit(invalid index) succeeded")
		}
		assertDeploymentIndexUnlocked(t, draft.repository)
	})

	t.Run("replacement", func(t *testing.T) {
		t.Parallel()

		draft := stagedDeploymentDraftFixture(t)
		if err := rollbackDeploymentEditWith(
			t.Context(), draft,
			func(string, string, []byte, []byte) (bool, error) {
				return false, errDeploymentCoverage
			},
			defaultDeploymentGitFileOperations(),
		); !errors.Is(err, errDeploymentCoverage) {
			t.Fatalf("rollbackDeploymentEditWith(replacement failure) error = %v", err)
		}
		assertDeploymentIndexUnlocked(t, draft.repository)
	})

	t.Run("index already reset", func(t *testing.T) {
		t.Parallel()

		draft := stagedDeploymentDraftFixture(t)
		if _, err := runGit(
			t.Context(), draft.repository, "reset", "--quiet", draft.base.tree, "--", draft.entry,
		); err != nil {
			t.Fatalf("git reset index error = %v", err)
		}
		if err := rollbackDeploymentEdit(t.Context(), draft); err != nil {
			t.Fatalf("rollbackDeploymentEdit(already reset index) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
		if _, err := cleanGitTree(t.Context(), draft.repository); err != nil {
			t.Fatalf("rollback with reset index left repository dirty: %v", err)
		}
		assertDeploymentIndexUnlocked(t, draft.repository)
	})

	t.Run("digest drift and failed compensation", func(t *testing.T) {
		t.Parallel()

		draft := stagedDeploymentDraftFixture(t)
		indexPath, err := absoluteGitPath(t.Context(), draft.repository, "index")
		if err != nil {
			t.Fatalf("absoluteGitPath(index) error = %v", err)
		}
		calls := 0
		replace := func(repository, entry string, before, after []byte) (bool, error) {
			calls++
			published, replaceErr := replaceDeploymentEntry(repository, entry, before, after)
			if calls == 1 && replaceErr == nil {
				file, openErr := os.OpenFile( //nolint:gosec // Git resolved this fixture-owned absolute index path.
					indexPath, os.O_WRONLY|os.O_APPEND, 0,
				)
				if openErr != nil {
					t.Fatalf("OpenFile(index) error = %v", openErr)
				}
				if _, writeErr := file.WriteString("drift"); writeErr != nil {
					t.Fatalf("WriteString(index) error = %v", writeErr)
				}
				if closeErr := file.Close(); closeErr != nil {
					t.Fatalf("Close(index) error = %v", closeErr)
				}
			}
			if calls == 2 {
				return published, errors.Join(replaceErr, errDeploymentCoverage)
			}

			return published, replaceErr
		}
		if err = rollbackDeploymentEditWith(
			t.Context(), draft, replace, defaultDeploymentGitFileOperations(),
		); !errors.Is(err, errDeploymentEditInvalid) || !errors.Is(err, errDeploymentCoverage) {
			t.Fatalf("rollbackDeploymentEditWith(digest drift) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.candidate.Content)
		assertDeploymentIndexUnlocked(t, draft.repository)
	})

	t.Run("index publication and lock cleanup", func(t *testing.T) {
		t.Parallel()

		draft := stagedDeploymentDraftFixture(t)
		operations := defaultDeploymentGitFileOperations()
		operations.rename = func(string, string) error { return errDeploymentCoverage }
		remove := operations.remove
		operations.remove = func(path string) error {
			return errors.Join(remove(path), errDeploymentCoverage)
		}
		if err := rollbackDeploymentEditWith(
			t.Context(), draft, replaceDeploymentEntry, operations,
		); !errors.Is(err, errDeploymentCoverage) {
			t.Fatalf("rollbackDeploymentEditWith(publication failure) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.candidate.Content)
		assertDeploymentIndexUnlocked(t, draft.repository)
	})
}

//nolint:paralleltest // Each subtest installs a process-wide PATH wrapper around Git.
func TestDeploymentRollbackRejectsResetFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		prefix string
	}{
		{
			name: "reset command",
			prefix: "for argument do\n" +
				"  if [ \"$argument\" = reset ]; then exit 1; fi\n" +
				"done\n",
		},
		{
			name: "reset result",
			prefix: "for argument do\n" +
				"  if [ \"$argument\" = reset ]; then exit 0; fi\n" +
				"done\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			draft := stagedDeploymentDraftFixture(t)
			installDeploymentGitWrapper(t, test.prefix)
			if err := rollbackDeploymentEdit(t.Context(), draft); err == nil {
				t.Fatal("rollbackDeploymentEdit(reset failure) succeeded")
			}
			assertDeploymentIndexUnlocked(t, draft.repository)
		})
	}
}

func stagedDeploymentDraftFixture(t *testing.T) tuiDeploymentDraft {
	t.Helper()

	draft := newDeploymentDraftFixture(t)
	staged, _, err := stageDeploymentEdit(t.Context(), draft)
	if err != nil {
		t.Fatalf("stageDeploymentEdit() error = %v", err)
	}

	return staged.draft
}

func assertDeploymentIndexUnlocked(t *testing.T, repository string) {
	t.Helper()

	indexPath, err := absoluteGitPath(t.Context(), repository, "index")
	if err != nil {
		t.Fatalf("absoluteGitPath(index) error = %v", err)
	}
	if _, err = os.Lstat(indexPath + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deployment index lock remained: %v", err)
	}
}

func TestTUIDeploymentWorkspaceFailedPreviewInvalidatesPriorConfirmation(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"manual edit", "LLM recommendation", "history restore"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
			if _, err := workspace.Preview(
				t.Context(), request, application.DeploymentCPUs.ID(), "2", false,
			); err != nil {
				t.Fatalf("initial Preview() error = %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var err error
			switch kind {
			case "manual edit":
				_, err = workspace.Preview(
					ctx, request, application.DeploymentMemory.ID(), "2048", false,
				)
			case "LLM recommendation":
				_, err = workspace.PreviewPatches(ctx, request, []application.DeploymentPatch{
					deploymentPatch(t, application.DeploymentMemory, "2048"),
				})
			case "history restore":
				var revision string
				revision, err = resolveGitObject(t.Context(), repository, "HEAD^{commit}")
				if err == nil {
					_, err = workspace.PreviewRestore(ctx, request, revision)
				}
			}
			if err == nil {
				t.Fatal("replacement preview succeeded")
			}
			if _, err := workspace.Stage(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
				t.Fatalf("Stage(after failed preview) error = %v", err)
			}
		})
	}
}

//nolint:cyclop // The test builds two revisions and proves a transform-normalized no-op end to end.
func TestTUIDeploymentRestoreNormalizesToNoOpBeforeMutation(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	historical := bytes.ReplaceAll(deploymentComposeFixture(), []byte("\n"), []byte("\r\n"))
	if err := os.WriteFile(path, historical, 0o600); err != nil {
		t.Fatalf("WriteFile(historical) error = %v", err)
	}
	commitApplyTestRepository(t, repository, deploymentComposeEntry)
	historicalRevision, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve historical HEAD error = %v", err)
	}
	if err = os.WriteFile(path, deploymentComposeFixture(), 0o600); err != nil {
		t.Fatalf("WriteFile(current) error = %v", err)
	}
	if err = os.WriteFile(
		filepath.Join(repository, ".gitattributes"),
		[]byte(deploymentComposeEntry+" text eol=lf\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(.gitattributes) error = %v", err)
	}
	commitApplyTestRepository(t, repository, deploymentComposeEntry, ".gitattributes")
	runtimeBase := t.TempDir()
	environment := map[string]string{testComposeDisableEnvFile: trueValue}
	source, err := loadTrackedComposeSource(t.Context(), path, repository, environment, runtimeBase)
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
	workspace := &tuiDeploymentWorkspace{environment: environment, runtimeBase: runtimeBase}
	preview, err := workspace.PreviewRestore(
		t.Context(), application.Request{Source: source, Service: applyServiceValue}, historicalRevision,
	)
	if err != nil || !preview.NoChanges || workspace.draft != nil {
		t.Fatalf("PreviewRestore(normalized no-op) = %#v, %v", preview, err)
	}
	assertTUIDeploymentContent(t, repository, deploymentComposeEntry, source.Content)
	if _, err = cleanGitTree(t.Context(), repository); err != nil {
		t.Fatalf("normalized no-op changed repository: %v", err)
	}
}

func TestTUIDeploymentWorkspaceRejectsUnsafeInputsAndHistory(t *testing.T) {
	t.Parallel()

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	for _, input := range []struct {
		field string
		value string
		unset bool
	}{
		{field: "unknown", value: "1"},
		{field: application.DeploymentCPUs.ID(), value: "0"},
		{field: application.DeploymentNoNewPrivileges.ID(), value: falseValue},
		{field: application.DeploymentNoNewPrivileges.ID(), unset: true},
	} {
		if _, err := workspace.Preview(
			t.Context(), request, input.field, input.value, input.unset,
		); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("Preview(%#v) error = %v", input, err)
		}
	}
	if _, err := workspace.PreviewRestore(t.Context(), request, strings.Repeat("a", 40)); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("PreviewRestore(unlisted) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, "drift"), []byte("drift"), 0o600); err != nil {
		t.Fatalf("WriteFile(drift) error = %v", err)
	}
	if _, err := workspace.Fields(t.Context(), request); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Fields(dirty repository) error = %v", err)
	}
}

func newTUIDeploymentWorkspaceFixture(
	t *testing.T,
	content []byte,
) (*tuiDeploymentWorkspace, application.Request, string) {
	t.Helper()

	repository := t.TempDir()
	path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	commitApplyTestRepository(t, repository, deploymentComposeEntry)
	runtimeBase := t.TempDir()
	environment := map[string]string{testComposeDisableEnvFile: trueValue}
	source, err := loadTrackedComposeSource(
		t.Context(), path, repository, environment, runtimeBase,
	)
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}

	return &tuiDeploymentWorkspace{environment: environment, runtimeBase: runtimeBase},
		application.Request{Source: source, Service: applyServiceValue}, repository
}

func assertTUIDeploymentContent(t *testing.T, repository, entry string, expected []byte) {
	t.Helper()

	actual, err := os.ReadFile( //nolint:gosec // Both values come from this test's temporary repository fixture.
		filepath.Join(repository, filepath.FromSlash(entry)),
	)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("Compose content changed:\n%s", actual)
	}
}
