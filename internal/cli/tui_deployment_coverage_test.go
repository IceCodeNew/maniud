package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

var errDeploymentCoverage = errors.New("deployment coverage failure")

const (
	testComposeDisableEnvFile   = "COMPOSE_DISABLE_ENV_FILE"
	testDeploymentCPUValue      = "2.5"
	testDeploymentDuration      = "30s"
	testDeploymentMissingEntry  = "missing.yaml"
	testDeploymentRestart       = "always"
	testDeploymentDirectorySync = "directory sync"
)

func TestParseDeploymentPatchCoversClosedFieldCatalog(t *testing.T) {
	t.Parallel()

	valid := map[application.DeploymentField]string{
		application.DeploymentCPUs:                testDeploymentCPUValue,
		application.DeploymentMemory:              "1048576",
		application.DeploymentPIDs:                "100",
		application.DeploymentRestart:             "unless-stopped",
		application.DeploymentSharedMemory:        "67108864",
		application.DeploymentStopGrace:           testDeploymentDuration,
		application.DeploymentInit:                trueValue,
		application.DeploymentReadOnly:            falseValue,
		application.DeploymentNoNewPrivileges:     trueValue,
		application.DeploymentHealthInterval:      testDeploymentDuration,
		application.DeploymentHealthTimeout:       "5s",
		application.DeploymentHealthRetries:       "3",
		application.DeploymentHealthStartPeriod:   "10s",
		application.DeploymentHealthStartInterval: "2s",
	}
	for _, field := range application.DeploymentFields() {
		patch, err := parseDeploymentPatch(field, valid[field], false)
		if err != nil || patch.Field() != field {
			t.Fatalf("parseDeploymentPatch(%s) = %#v, %v", field.ID(), patch, err)
		}
	}
	if patch, err := parseDeploymentPatch(application.DeploymentCPUs, "", true); err != nil ||
		patch.Field() != application.DeploymentCPUs {
		t.Fatalf("parseDeploymentPatch(unset) = %#v, %v", patch, err)
	}

	invalid := []struct {
		field application.DeploymentField
		value string
		unset bool
	}{
		{field: application.DeploymentNoNewPrivileges, unset: true},
		{field: application.DeploymentCPUs},
		{field: application.DeploymentCPUs, value: strings.Repeat("1", maximumTUICommitMessage+1)},
		{field: application.DeploymentCPUs, value: " 2"},
		{field: application.DeploymentCPUs, value: "2\n"},
		{field: application.DeploymentCPUs, value: "x"},
		{field: application.DeploymentMemory, value: "x"},
		{field: application.DeploymentPIDs, value: "x"},
		{field: application.DeploymentHealthRetries, value: "x"},
		{field: application.DeploymentStopGrace, value: "x"},
		{field: application.DeploymentInit, value: "x"},
		{field: application.DeploymentNoNewPrivileges, value: falseValue},
		{field: application.DeploymentField(255), value: "1"},
	}
	for _, input := range invalid {
		if _, err := parseDeploymentPatch(input.field, input.value, input.unset); !errors.Is(
			err, errDeploymentEditInvalid,
		) {
			t.Fatalf("parseDeploymentPatch(%#v) error = %v", input, err)
		}
	}
}

func TestParseDeploymentHistoryRejectsMalformedProtocol(t *testing.T) {
	t.Parallel()

	validRevisionValue := strings.Repeat("a", 40)
	valid := []byte(validRevisionValue + "\x00subject\x00")
	if history, err := parseDeploymentHistory(valid); err != nil || len(history) != 1 ||
		history[0].SignaturePresent {
		t.Fatalf("parseDeploymentHistory(valid) = %#v, %v", history, err)
	}
	tooMany := bytes.Repeat([]byte(validRevisionValue+"\x00subject\x00"), maximumDeploymentHistory+1)
	invalid := [][]byte{
		[]byte("unterminated"),
		[]byte(validRevisionValue + "\x00"),
		tooMany,
		[]byte("bad\x00subject\x00"),
		[]byte(validRevisionValue + "\x00\x00"),
		append([]byte(validRevisionValue+"\x00"), 0xff, 0),
	}
	for _, output := range invalid {
		if _, err := parseDeploymentHistory(output); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("parseDeploymentHistory(%q) error = %v", output, err)
		}
	}
}

func TestDeploymentHistoryRejectsMalformedLogAndSignatureReadFailure(t *testing.T) {
	t.Parallel()

	revision := strings.Repeat("a", 40)
	for _, test := range []struct {
		name      string
		output    []byte
		signature func(context.Context, string, string) (bool, error)
	}{
		{
			name:   "malformed log",
			output: []byte("malformed\x00subject\x00"),
			signature: func(context.Context, string, string) (bool, error) {
				return false, nil
			},
		},
		{
			name:   "signature read",
			output: []byte(revision + "\x00subject\x00"),
			signature: func(context.Context, string, string) (bool, error) {
				return false, errDeploymentCoverage
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := deploymentHistoryWith(
				t.Context(), t.TempDir(), deploymentComposeEntry,
				func(context.Context, string, ...string) ([]byte, error) {
					return test.output, nil
				},
				test.signature,
			)
			if !errors.Is(err, errDeploymentEditInvalid) {
				t.Fatalf("deploymentHistoryWith() error = %v", err)
			}
		})
	}
}

//nolint:paralleltest // This test temporarily replaces PATH with a deterministic Git fault injector.
func TestDeploymentHistoryClassifiesGitLogFailure(t *testing.T) {
	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	installDeploymentGitWrapper(t, ""+
		"for argument do\n"+
		"  if [ \"$argument\" = log ]; then exit 1; fi\n"+
		"done\n")

	if _, err := workspace.History(t.Context(), request); !isDeploymentFailure(
		err, tui.DeploymentHistoryUnavailable,
	) {
		t.Fatalf("History(git log failure) error = %v", err)
	}
}

func TestDeploymentAttributeOutputPolicy(t *testing.T) {
	t.Parallel()

	const entry = "services/compose.yaml"
	for _, test := range []struct {
		name   string
		output string
		want   bool
	}{
		{name: "empty", want: true},
		{name: "built in", output: entry + "\x00text\x00set\x00", want: true},
		{name: "filter unset", output: entry + "\x00filter\x00unset\x00", want: true},
		{name: "filter unspecified", output: entry + "\x00filter\x00unspecified\x00", want: true},
		{name: "filter driver", output: entry + "\x00filter\x00driver\x00"},
		{name: "wrong path", output: "other.yaml\x00text\x00set\x00"},
		{name: "invalid width", output: entry + "\x00text\x00"},
		{name: "unterminated", output: entry},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if actual := deploymentAttributeOutputSafe([]byte(test.output), entry); actual != test.want {
				t.Fatalf("deploymentAttributeOutputSafe() = %t, want %t", actual, test.want)
			}
		})
	}
}

func TestValidatedStagedDeploymentContentRejectsInvalidSource(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		content []byte
		service string
		entry   string
		cancel  bool
	}{
		{
			name: "missing entry", content: deploymentComposeFixture(),
			service: applyServiceValue, entry: testDeploymentMissingEntry,
		},
		{name: "read error", content: deploymentComposeFixture(), service: applyServiceValue, cancel: true},
		{name: "compose load", content: deploymentUnsupportedFixture(), service: applyServiceValue},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			repository := t.TempDir()
			entry := deploymentComposeEntry
			path := filepath.Join(repository, filepath.FromSlash(entry))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			commitApplyTestRepository(t, repository, entry)
			state, err := cleanGitTree(t.Context(), repository)
			if err != nil {
				t.Fatalf("cleanGitTree() error = %v", err)
			}
			if test.entry != "" {
				entry = test.entry
			}
			ctx := t.Context()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			draft := tuiDeploymentDraft{
				request:   application.Request{Service: test.service},
				candidate: compose.Source{Content: bytes.Clone(test.content)},
				entry:     entry,
			}
			proof := tuiStagedProof{repository: repository, expectedTree: state.tree}
			if _, err = validatedStagedDeploymentContent(ctx, draft, proof); !errors.Is(
				err, errDeploymentEditInvalid,
			) {
				t.Fatalf("validatedStagedDeploymentContent() error = %v", err)
			}
		})
	}
	draft := newDeploymentDraftFixture(t)
	draft.request.Service = "missing"
	if _, _, err := stageDeploymentEdit(t.Context(), draft); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("stageDeploymentEdit(missing service) error = %v", err)
	}
}

//nolint:cyclop,funlen,gocognit,gocyclo // One repository sequence exercises each request-scope boundary.
func TestDeploymentWorkspaceValidatesComposeAndRequestScope(t *testing.T) {
	t.Parallel()

	missingService := deploymentOtherServiceFixture()
	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, missingService)
	if _, err := workspace.Fields(t.Context(), request); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Fields(missing service) error = %v", err)
	}
	if _, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), "2", false,
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Preview(missing service) error = %v", err)
	}
	invalidWorkspace, invalidRequest, _ := newTUIDeploymentWorkspaceFixture(t, deploymentUnsupportedFixture())
	if _, err := invalidWorkspace.Fields(t.Context(), invalidRequest); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Fields(unsupported Compose) error = %v", err)
	}

	validWorkspace, validRequest, validRepository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := validWorkspace.Preview(
		t.Context(), application.Request{}, application.DeploymentCPUs.ID(), "2", false,
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Preview(invalid request) error = %v", err)
	}
	for _, invalid := range []application.Request{
		{Service: applyServiceValue},
		{
			Service: applyServiceValue,
			Source: compose.Source{Repository: &compose.RepositorySnapshot{
				Root: testRelativePath, Entry: deploymentComposeEntry,
			}},
		},
		{
			Service: applyServiceValue,
			Source: compose.Source{Repository: &compose.RepositorySnapshot{
				Root: repository, Entry: "../compose.yaml",
			}},
		},
	} {
		if _, _, _, _, err := validWorkspace.openRequest(t.Context(), invalid); !errors.Is(
			err, errDeploymentEditInvalid,
		) {
			t.Fatalf("openRequest(%#v) error = %v", invalid, err)
		}
	}
	validBranch, err := currentGitBranch(t.Context(), validRepository)
	if err != nil {
		t.Fatalf("currentGitBranch(valid) error = %v", err)
	}
	validScope, err := compose.NewRepositoryScope(validRepository, validRepository, validBranch)
	if err != nil {
		t.Fatalf("NewRepositoryScope(valid) error = %v", err)
	}
	validRequest.Repository, err = validScope.Bind(
		deploymentComposeEntry, validRequest.Source.Repository.Digest,
	)
	if err != nil {
		t.Fatalf("Bind(valid) error = %v", err)
	}
	validWorkspace.registrationPath = filepath.Join(t.TempDir(), gitOpsRegistrationName)
	if _, err = validWorkspace.Preview(
		t.Context(), validRequest, application.DeploymentCPUs.ID(), "2", false,
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Preview(scope unavailable) error = %v", err)
	}

	branch, err := currentGitBranch(t.Context(), repository)
	if err != nil {
		t.Fatalf("currentGitBranch() error = %v", err)
	}
	scope, err := compose.NewRepositoryScope(repository, repository, branch)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	provenance, err := scope.Bind(deploymentComposeEntry, request.Source.Repository.Digest)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	request.Repository = provenance
	workspace.registrationPath = filepath.Join(t.TempDir(), gitOpsRegistrationName)
	if _, err = workspace.requestScope(t.Context(), request, request.Source); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("requestScope(missing registration) error = %v", err)
	}
	head, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve HEAD error = %v", err)
	}
	if _, err = runGit(t.Context(), repository, "remote", "add", gitOpsRemoteName, repository); err != nil {
		t.Fatalf("git remote add error = %v", err)
	}
	registration := gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: repository, Branch: branch,
		Remote: gitOpsRemoteName, RemoteURL: repository, BaselineCommit: head,
	}
	if err = writeGitOpsRegistration(workspace.registrationPath, registration); err != nil {
		t.Fatalf("writeGitOpsRegistration() error = %v", err)
	}
	actual, scopeErr := workspace.requestScope(t.Context(), request, request.Source)
	if scopeErr != nil || !actual.Valid() {
		t.Fatalf("requestScope(valid) = %#v, %v", actual, scopeErr)
	}
	request.Repository = compose.RepositoryProvenance{}
	actual, scopeErr = workspace.requestScope(t.Context(), request, request.Source)
	if scopeErr != nil || actual.Valid() {
		t.Fatalf("requestScope(unbound) = %#v, %v", actual, scopeErr)
	}

	request.Repository = provenance
	wrong := registration
	wrong.Repository = t.TempDir()
	workspace.registrationPath = filepath.Join(t.TempDir(), gitOpsRegistrationName)
	if err = writeGitOpsRegistration(workspace.registrationPath, wrong); err != nil {
		t.Fatalf("write mismatched registration error = %v", err)
	}
	if _, err = workspace.requestScope(t.Context(), request, request.Source); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("requestScope(repository mismatch) error = %v", err)
	}

	workspace.registrationPath = filepath.Join(t.TempDir(), gitOpsRegistrationName)
	if err = writeGitOpsRegistration(workspace.registrationPath, registration); err != nil {
		t.Fatalf("write remote registration error = %v", err)
	}
	if _, err = runGit(t.Context(), repository, "remote", "remove", gitOpsRemoteName); err != nil {
		t.Fatalf("git remote remove error = %v", err)
	}
	if _, err = workspace.requestScope(t.Context(), request, request.Source); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("requestScope(remote mismatch) error = %v", err)
	}
	if _, err = runGit(t.Context(), repository, "remote", "add", gitOpsRemoteName, repository); err != nil {
		t.Fatalf("git remote restore error = %v", err)
	}

	workspace.registrationPath = filepath.Join(t.TempDir(), gitOpsRegistrationName)
	if err = writeGitOpsRegistration(workspace.registrationPath, registration); err != nil {
		t.Fatalf("rewrite registration error = %v", err)
	}
	otherScope, err := compose.NewRepositoryScope(repository, "file:///different", branch)
	if err != nil {
		t.Fatalf("NewRepositoryScope(other) error = %v", err)
	}
	request.Repository, err = otherScope.Bind(deploymentComposeEntry, request.Source.Repository.Digest)
	if err != nil {
		t.Fatalf("Bind(other) error = %v", err)
	}
	if _, err = workspace.requestScope(t.Context(), request, request.Source); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("requestScope(provenance mismatch) error = %v", err)
	}
}

func deploymentOtherServiceFixture() []byte {
	return []byte("name: example\nservices:\n  worker:\n    container_name: example-worker\n" +
		"    image: example.invalid/worker:latest\n    network_mode: bridge\n")
}

func deploymentUnsupportedFixture() []byte {
	return []byte("name: example\nservices:\n  api:\n    container_name: example-api\n" +
		"    image: example.invalid/api:latest\n    network_mode: bridge\n    unsupported_field: true\n")
}

//nolint:paralleltest // This test temporarily replaces PATH with a deterministic Git fault injector.
func TestDeploymentWorkspaceRejectsGitStateLostBetweenProofSteps(t *testing.T) {
	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	statusCount := filepath.Join(t.TempDir(), "status-count")
	installDeploymentGitWrapper(t, ""+
		"status=0\n"+
		"for argument do\n"+
		"  if [ \"$argument\" = --porcelain=v1 ]; then status=1; fi\n"+
		"done\n"+
		"if [ \"$status\" -eq 1 ]; then\n"+
		"  count=$(cat "+shellArgument(statusCount)+" 2>/dev/null || printf 0)\n"+
		"  count=$((count + 1))\n"+
		"  printf '%s\\n' \"$count\" >"+shellArgument(statusCount)+"\n"+
		"  if [ \"$count\" -eq 3 ]; then exit 1; fi\n"+
		"fi\n")
	if _, _, _, _, err := workspace.openRequest(t.Context(), request); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("openRequest(lost state) error = %v", err)
	}
}

//nolint:paralleltest // This test temporarily replaces PATH with a deterministic Git fault injector.
func TestDeploymentRestoreRejectsRevisionLostAfterHistoryProof(t *testing.T) {
	workspace, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	initial, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve initial HEAD error = %v", err)
	}
	updated := bytes.Replace(deploymentComposeFixture(), []byte("cpus: 1"), []byte("cpus: 2"), 1)
	commitDeploymentContent(t, repository, updated, "update deployment")
	path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	runtimeBase := t.TempDir()
	environment := map[string]string{testComposeDisableEnvFile: trueValue}
	source, err := loadTrackedComposeSource(t.Context(), path, repository, environment, runtimeBase)
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
	workspace.environment = environment
	workspace.runtimeBase = runtimeBase
	installDeploymentGitWrapper(t, ""+
		"for argument do\n"+
		"  if [ \"$argument\" = "+shellArgument(initial+"^{tree}")+" ]; then exit 1; fi\n"+
		"done\n")
	if _, err = workspace.PreviewRestore(
		t.Context(), application.Request{Source: source, Service: applyServiceValue}, initial,
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("PreviewRestore(lost revision) error = %v", err)
	}
}

//nolint:paralleltest // This test temporarily replaces PATH with a deterministic Git fault injector.
func TestDeploymentRestoreClassifiesConfirmationFailure(t *testing.T) {
	workspace, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	initial, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve HEAD error = %v", err)
	}
	updated := bytes.Replace(deploymentComposeFixture(), []byte("cpus: 1"), []byte("cpus: 2"), 1)
	commitDeploymentContent(t, repository, updated, "update deployment")
	path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	runtimeBase := t.TempDir()
	environment := map[string]string{testComposeDisableEnvFile: trueValue}
	source, err := loadTrackedComposeSource(t.Context(), path, repository, environment, runtimeBase)
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
	workspace.environment = environment
	workspace.runtimeBase = runtimeBase
	installDeploymentGitWrapper(t, ""+
		"for argument do\n"+
		"  if [ \"$argument\" = --cached ]; then exit 1; fi\n"+
		"done\n")

	if _, err = workspace.PreviewRestore(
		t.Context(), application.Request{Source: source, Service: applyServiceValue}, initial,
	); !isDeploymentFailure(err, tui.DeploymentPreconditionFailed) {
		t.Fatalf("PreviewRestore(confirmation failure) error = %v", err)
	}
}

func installDeploymentGitWrapper(t *testing.T, prefix string) {
	t.Helper()

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath(git) error = %v", err)
	}
	directory := t.TempDir()
	wrapper := filepath.Join(directory, "git")
	script := "#!/bin/sh\n" + prefix + "exec " + shellArgument(realGit) + " \"$@\"\n"
	//nolint:gosec // The private test fixture must be executable.
	if err = os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("WriteFile(git wrapper) error = %v", err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDeploymentWorkspaceDiscardClearsDraft(t *testing.T) {
	t.Parallel()

	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), "2", false,
	); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if err := workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard(draft) error = %v", err)
	}
	if workspace.draft != nil {
		t.Fatal("Discard(draft) retained draft")
	}
}

func TestDeploymentWorkspaceReturnsTypedNoChangeBeforeGitPreview(t *testing.T) {
	t.Parallel()

	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	preview, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), "1", false,
	)
	if err != nil || !preview.NoChanges || workspace.draft != nil {
		t.Fatalf("Preview(no change) = %#v, %v", preview, err)
	}
}

func TestDeploymentPreviewAndStageContainMissingConfirmationProof(t *testing.T) {
	t.Parallel()

	draft := newDeploymentDraftFixture(t)
	workspace := &tuiDeploymentWorkspace{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := workspace.confirmDeploymentPreview(ctx, draft); err == nil {
		t.Fatal("confirmDeploymentPreview(cancelled) succeeded")
	}
	draft.confirmation.expectedTree = ""
	if _, _, err := stageDeploymentEdit(t.Context(), draft); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("stageDeploymentEdit(missing confirmation) error = %v", err)
	}
}

//nolint:paralleltest // The test installs a process-wide PATH wrapper around Git.
func TestDeploymentRestoreContainsBlobReadFailure(t *testing.T) {
	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	revision, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve HEAD error = %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "cat-file-count")
	installDeploymentGitWrapper(t, ""+
		"for argument do\n"+
		"  if [ \"$argument\" = cat-file ]; then\n"+
		"    count=$(cat "+shellArgument(countPath)+" 2>/dev/null || printf 0)\n"+
		"    count=$((count + 1))\n"+
		"    printf '%s\\n' \"$count\" >"+shellArgument(countPath)+"\n"+
		"    if [ \"$count\" -eq 3 ]; then exit 1; fi\n"+
		"  fi\n"+
		"done\n")
	if _, err = workspace.PreviewRestore(t.Context(), request, revision); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("PreviewRestore(blob read failure) error = %v", err)
	}
}

//nolint:cyclop // One stateful sequence covers each invalid deployment lifecycle transition.
func TestDeploymentWorkspaceRejectsInvalidLifecycleStates(t *testing.T) {
	t.Parallel()

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := workspace.Stage(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Stage(without draft) error = %v", err)
	}
	if err := workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard(without stage) error = %v", err)
	}
	if _, err := workspace.History(t.Context(), application.Request{}); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("History(invalid request) error = %v", err)
	}
	if _, err := workspace.PreviewRestore(
		t.Context(), application.Request{}, strings.Repeat("a", 40),
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("PreviewRestore(invalid request) error = %v", err)
	}

	if _, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), "2", false,
	); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	workspace.staged = &tuiStagedDeployment{}
	if _, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), "3", false,
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Preview(staged) error = %v", err)
	}
	if _, err := workspace.History(t.Context(), request); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("History(staged) error = %v", err)
	}
	if _, err := workspace.PreviewRestore(
		t.Context(), request, strings.Repeat("a", 40),
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("PreviewRestore(staged) error = %v", err)
	}
	if _, err := workspace.Stage(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Stage(staged) error = %v", err)
	}
	workspace.staged = nil
	if err := os.WriteFile(filepath.Join(repository, "drift"), []byte("drift"), 0o600); err != nil {
		t.Fatalf("WriteFile(drift) error = %v", err)
	}
	if _, err := workspace.Stage(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Stage(dirty repository) error = %v", err)
	}

	if _, err := deploymentHistory(t.Context(), filepath.Join(repository, "missing"), deploymentComposeEntry); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("deploymentHistory(missing repository) error = %v", err)
	}
}

//nolint:cyclop // One filesystem fixture covers each publication and rollback proof rejection.
func TestDeploymentFilesystemProofRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	invalidDefault := defaultTUIDeploymentWorkspace(map[string]string{
		homeKey: "", xdgStateHomeKey: testRelativePath,
	})
	if invalidDefault.registrationPath != "" || invalidDefault.runtimeBase != "" {
		t.Fatalf("defaultTUIDeploymentWorkspace(invalid state path) = %#v", invalidDefault)
	}
	invalidRepository := filepath.Join(t.TempDir(), "missing")
	if published, err := replaceDeploymentEntry(invalidRepository, composeFileValue, nil, nil); published ||
		!errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("replaceDeploymentEntry(missing repository) = %t, %v", published, err)
	}

	repository := t.TempDir()
	if published, err := replaceDeploymentEntry(repository, testDeploymentMissingEntry, nil, nil); published ||
		!errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("replaceDeploymentEntry(missing file) = %t, %v", published, err)
	}
	if err := os.Mkdir(filepath.Join(repository, "directory"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if published, err := replaceDeploymentEntry(repository, "directory", nil, nil); published ||
		!errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("replaceDeploymentEntry(directory) = %t, %v", published, err)
	}
	path := filepath.Join(repository, composeFileValue)
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if published, err := replaceDeploymentEntry(
		repository, composeFileValue, []byte("other"), []byte("after"),
	); published || !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("replaceDeploymentEntry(content mismatch) = %t, %v", published, err)
	}
	if published, err := replaceDeploymentEntry(
		repository, composeFileValue, []byte("before"), []byte("after"),
	); !published || err != nil {
		t.Fatalf("replaceDeploymentEntry(valid) = %t, %v", published, err)
	}

	badProof := tuiStagedProof{repository: repository, expectedTree: "bad"}
	if deploymentTreeContains(t.Context(), badProof, composeFileValue, []byte("after")) {
		t.Fatal("deploymentTreeContains(invalid proof) = true")
	}
	draft := tuiDeploymentDraft{
		repository: repository,
		entry:      composeFileValue,
		base:       gitTreeState{head: strings.Repeat("a", 40), tree: strings.Repeat("b", 40)},
		source:     compose.Source{Content: []byte("before")},
		candidate:  compose.Source{Content: []byte("after")},
	}
	if err := rollbackDeploymentEdit(t.Context(), draft); err == nil {
		t.Fatal("rollbackDeploymentEdit(invalid repository) succeeded")
	}
}

func TestDeploymentStagedProofRejectsInvalidGitState(t *testing.T) {
	t.Parallel()

	nonRepository := t.TempDir()
	if exactStagedPathStatusWithAttributes(
		t.Context(), nonRepository, []string{composeFileValue}, "M", "",
	) {
		t.Fatal("exactStagedPathStatusWithAttributes(non-repository) = true")
	}
	if head, unchanged := proveTUIStagedCommit(
		t.Context(), tuiStagedProof{repository: nonRepository}, "Update deployment", false,
	); head != "" || unchanged {
		t.Fatalf("proveTUIStagedCommit(non-repository) = %q, %t", head, unchanged)
	}

	draft := newDeploymentDraftFixture(t)
	proof := tuiStagedProof{
		repository: draft.repository,
		base:       gitTreeState{head: strings.Repeat("a", 40)},
	}
	if head, unchanged := proveTUIStagedCommit(t.Context(), proof, "Update deployment", false); head != "" || unchanged {
		t.Fatalf("proveTUIStagedCommit(mismatched parent) = %q, %t", head, unchanged)
	}
}

//nolint:cyclop,funlen,gocognit // The table exercises each publication failure boundary through the private seam.
func TestReplaceDeploymentEntryFailureBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		published bool
		configure func(*deploymentEntryOperations)
	}{
		{
			name: "open root",
			configure: func(operations *deploymentEntryOperations) {
				operations.openRoot = func(string) (*os.Root, error) { return nil, errDeploymentCoverage }
			},
		},
		{
			name: "lstat",
			configure: func(operations *deploymentEntryOperations) {
				operations.lstat = func(*os.Root, string) (os.FileInfo, error) {
					return nil, errDeploymentCoverage
				}
			},
		},
		{
			name: "initial read",
			configure: func(operations *deploymentEntryOperations) {
				operations.readFile = func(*os.File) ([]byte, error) {
					return nil, errDeploymentCoverage
				}
			},
		},
		{
			name: createOperation,
			configure: func(operations *deploymentEntryOperations) {
				operations.openFile = func(*os.Root, string, os.FileMode) (*os.File, error) {
					return nil, errDeploymentCoverage
				}
			},
		},
		{
			name: "write",
			configure: func(operations *deploymentEntryOperations) {
				operations.writeFile = func(*os.File, []byte) (int, error) {
					return 0, errDeploymentCoverage
				}
			},
		},
		{
			name: "short write",
			configure: func(operations *deploymentEntryOperations) {
				operations.writeFile = func(*os.File, []byte) (int, error) { return 0, nil }
			},
		},
		{
			name: "file sync",
			configure: func(operations *deploymentEntryOperations) {
				operations.syncFile = func(*os.File) error { return errDeploymentCoverage }
			},
		},
		{
			name: "file close",
			configure: func(operations *deploymentEntryOperations) {
				operations.closeFile = func(file *os.File) error {
					return errors.Join(file.Close(), errDeploymentCoverage)
				}
			},
		},
		{
			name: "candidate read",
			configure: func(operations *deploymentEntryOperations) {
				calls := 0
				operations.readFile = func(file *os.File) ([]byte, error) {
					calls++
					if calls == 2 {
						return nil, errDeploymentCoverage
					}

					return io.ReadAll(file)
				}
			},
		},
		{
			name: "candidate mismatch",
			configure: func(operations *deploymentEntryOperations) {
				calls := 0
				operations.readFile = func(file *os.File) ([]byte, error) {
					calls++
					if calls == 2 {
						return []byte("drift"), nil
					}

					return io.ReadAll(file)
				}
			},
		},
		{
			name: "exchange",
			configure: func(operations *deploymentEntryOperations) {
				operations.exchange = func(*os.File, string, string) error { return errDeploymentCoverage }
			},
		},
		{
			name: "open directory",
			configure: func(operations *deploymentEntryOperations) {
				operations.openDirectory = func(*os.Root, string) (*os.File, error) {
					return nil, errDeploymentCoverage
				}
			},
		},
		{
			name:      testDeploymentDirectorySync,
			published: true,
			configure: func(operations *deploymentEntryOperations) {
				calls := 0
				operations.syncFile = func(file *os.File) error {
					calls++
					if calls == 2 {
						return errDeploymentCoverage
					}

					return file.Sync()
				}
			},
		},
		{
			name:      testDirectoryClose,
			published: true,
			configure: func(operations *deploymentEntryOperations) {
				calls := 0
				operations.closeFile = func(file *os.File) error {
					calls++
					if calls == 2 {
						return errors.Join(file.Close(), errDeploymentCoverage)
					}

					return file.Close()
				}
			},
		},
		{
			name:      "readback error",
			published: true,
			configure: func(operations *deploymentEntryOperations) {
				calls := 0
				operations.readFile = func(file *os.File) ([]byte, error) {
					calls++
					if calls == 4 {
						return nil, errDeploymentCoverage
					}

					return io.ReadAll(file)
				}
			},
		},
		{
			name:      "readback mismatch",
			published: true,
			configure: func(operations *deploymentEntryOperations) {
				calls := 0
				operations.readFile = func(file *os.File) ([]byte, error) {
					calls++
					if calls == 4 {
						return []byte("drift"), nil
					}

					return io.ReadAll(file)
				}
			},
		},
		{
			name:      "rollback exchange",
			published: true,
			configure: func(operations *deploymentEntryOperations) {
				readCalls := 0
				operations.readFile = func(file *os.File) ([]byte, error) {
					readCalls++
					if readCalls == 3 {
						return []byte("drift"), nil
					}

					return io.ReadAll(file)
				}
				exchange := operations.exchange
				exchangeCalls := 0
				operations.exchange = func(directory *os.File, first, second string) error {
					exchangeCalls++
					if exchangeCalls == 2 {
						return errDeploymentCoverage
					}

					return exchange(directory, first, second)
				}
			},
		},
		{
			name:      testRootClose,
			published: true,
			configure: func(operations *deploymentEntryOperations) {
				operations.closeRoot = func(root *os.Root) error {
					return errors.Join(root.Close(), errDeploymentCoverage)
				}
			},
		},
		{
			name: "cleanup remove",
			configure: func(operations *deploymentEntryOperations) {
				operations.writeFile = func(*os.File, []byte) (int, error) {
					return 0, errDeploymentCoverage
				}
				operations.remove = func(*os.Root, string) error { return errDeploymentCoverage }
			},
		},
		{
			name:      "remove displaced entry",
			published: true,
			configure: func(operations *deploymentEntryOperations) {
				operations.remove = func(*os.Root, string) error { return errDeploymentCoverage }
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			repository := t.TempDir()
			if err := os.WriteFile(filepath.Join(repository, composeFileValue), []byte("before"), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			operations := defaultDeploymentEntryOperations()
			test.configure(&operations)
			published, err := replaceDeploymentEntryWithOperations(
				repository, composeFileValue, []byte("before"), []byte("after"), operations,
			)
			if err == nil || published != test.published {
				t.Fatalf("replaceDeploymentEntryWithOperations() = %t, %v", published, err)
			}
		})
	}
}

func TestExchangeDeploymentEntriesRejectsMissingEntry(t *testing.T) {
	t.Parallel()

	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := directory.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})
	if err = exchangeDeploymentEntries(directory, "missing-first", "missing-second"); err == nil {
		t.Fatal("exchangeDeploymentEntries(missing) succeeded")
	}
}

//nolint:cyclop // The ordered path swaps reproduce one descriptor-versus-path lookup race.
func TestReplaceDeploymentEntryReadsTheRetainedDescriptor(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	path := filepath.Join(repository, composeFileValue)
	if err := os.WriteFile(path, []byte("foreign"), 0o600); err != nil {
		t.Fatalf("WriteFile(foreign) error = %v", err)
	}
	operations := defaultDeploymentEntryOperations()
	exchange := operations.exchange
	exchangeCalls := 0
	operations.exchange = func(directory *os.File, first, second string) error {
		exchangeCalls++

		return exchange(directory, first, second)
	}
	lstat := operations.lstat
	lstatCalls := 0
	operations.lstat = func(root *os.Root, name string) (os.FileInfo, error) {
		lstatCalls++
		if lstatCalls == 1 {
			info, err := lstat(root, name)
			if err != nil {
				return nil, err
			}
			if err = os.Rename(path, path+".held"); err != nil {
				return nil, fmt.Errorf("retain opened entry: %w", err)
			}
			if err = os.WriteFile(path, []byte("before"), 0o600); err != nil {
				return nil, fmt.Errorf("write temporary replacement: %w", err)
			}

			return info, nil
		}
		if lstatCalls == 2 {
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("remove temporary replacement: %w", err)
			}
			if err := os.Rename(path+".held", path); err != nil {
				return nil, fmt.Errorf("restore opened entry: %w", err)
			}
		}

		return lstat(root, name)
	}
	published, err := replaceDeploymentEntryWithOperations(
		repository, composeFileValue, []byte("before"), []byte("after"), operations,
	)
	content, readErr := os.ReadFile(path) //nolint:gosec // The path is rooted in t.TempDir.
	if !errors.Is(err, errDeploymentEditInvalid) || published || readErr != nil ||
		string(content) != "foreign" || exchangeCalls != 0 {
		t.Fatalf(
			"descriptor-bound replacement = published %t, err %v, content %q, read %v, exchanges %d",
			published, err, content, readErr, exchangeCalls,
		)
	}
}

//nolint:cyclop // One assertion proves the replacement, publication state, and cleanup together.
func TestReplaceDeploymentEntryPreservesConcurrentReplacement(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	path := filepath.Join(repository, composeFileValue)
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatalf("WriteFile(before) error = %v", err)
	}
	operations := defaultDeploymentEntryOperations()
	exchange := operations.exchange
	firstExchange := true
	var replacement os.FileInfo
	operations.exchange = func(directory *os.File, first, second string) error {
		if firstExchange {
			firstExchange = false
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove concurrent source: %w", err)
			}
			if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
				return fmt.Errorf("write concurrent source: %w", err)
			}
			var err error
			replacement, err = os.Lstat(path)
			if err != nil {
				return fmt.Errorf("inspect concurrent source: %w", err)
			}
		}

		return exchange(directory, first, second)
	}
	published, err := replaceDeploymentEntryWithOperations(
		repository, composeFileValue, []byte("before"), []byte("after"), operations,
	)
	content, readErr := os.ReadFile(path) //nolint:gosec // The path is rooted in t.TempDir.
	current, statErr := os.Lstat(path)
	temporary, globErr := filepath.Glob(filepath.Join(repository, "."+composeFileValue+".maniud-*"))
	if err == nil || published || readErr != nil || string(content) != "before" || statErr != nil ||
		replacement == nil || !os.SameFile(replacement, current) ||
		globErr != nil || len(temporary) != 0 {
		t.Fatalf(
			"concurrent replacement = published %t, err %v, content %q, read/stat %v/%v, same %t, temporary %q, glob %v",
			published, err, content, readErr, statErr, replacement != nil && current != nil &&
				os.SameFile(replacement, current), temporary, globErr,
		)
	}
}

//nolint:cyclop // One assertion proves the replacement, publication state, and rescue together.
func TestReplaceDeploymentEntryPreservesPostExchangeDriftAsRescue(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	path := filepath.Join(repository, composeFileValue)
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatalf("WriteFile(before) error = %v", err)
	}
	operations := defaultDeploymentEntryOperations()
	exchange := operations.exchange
	operations.exchange = func(directory *os.File, first, second string) error {
		if err := exchange(directory, first, second); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove published candidate: %w", err)
		}

		return os.WriteFile(path, []byte("foreign"), 0o600)
	}
	published, err := replaceDeploymentEntryWithOperations(
		repository, composeFileValue, []byte("before"), []byte("after"), operations,
	)
	content, readErr := os.ReadFile(path) //nolint:gosec // The path is rooted in t.TempDir.
	rescues, globErr := filepath.Glob(filepath.Join(repository, "."+composeFileValue+".maniud-*"))
	if err == nil || !published || readErr != nil || string(content) != "foreign" ||
		globErr != nil || len(rescues) != 1 {
		t.Fatalf(
			"post-exchange drift = published %t, err %v, content %q, read %v, rescues %q, glob %v",
			published, err, content, readErr, rescues, globErr,
		)
	}
	rescue, rescueErr := os.ReadFile(rescues[0])
	if rescueErr != nil || string(rescue) != "before" {
		t.Fatalf("rescue = %q, %v", rescue, rescueErr)
	}
}

//nolint:cyclop,funlen // Each case stops at a distinct transaction proof boundary and must verify rescue.
func TestStageDeploymentEditFailureBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("repository drift", func(t *testing.T) {
		t.Parallel()

		draft := newDeploymentDraftFixture(t)
		if err := os.WriteFile(filepath.Join(draft.repository, "drift"), []byte("drift"), 0o600); err != nil {
			t.Fatalf("WriteFile(drift) error = %v", err)
		}
		if _, _, err := stageDeploymentEdit(t.Context(), draft); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("stageDeploymentEdit(drift) error = %v", err)
		}
	})

	tests := []struct {
		name      string
		configure func(t *testing.T, draft *tuiDeploymentDraft) (
			func(string, string, []byte, []byte) (bool, error),
			func(context.Context, string, []string) ([]byte, error),
			func(context.Context, string) (string, error),
		)
	}{
		{
			name: "unpublished replacement",
			configure: func(_ *testing.T, _ *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				return func(string, string, []byte, []byte) (bool, error) {
					return false, errDeploymentCoverage
				}, stagedTUIDiff, writeGitTree
			},
		},
		{
			name: "published replacement",
			configure: func(_ *testing.T, _ *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				return func(repository, entry string, before, after []byte) (bool, error) {
					published, err := replaceDeploymentEntry(repository, entry, before, after)

					return published, errors.Join(err, errDeploymentCoverage)
				}, stagedTUIDiff, writeGitTree
			},
		},
		{
			name: "empty index change",
			configure: func(_ *testing.T, draft *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				draft.candidate = draft.source

				return replaceDeploymentEntry, stagedTUIDiff, writeGitTree
			},
		},
		{
			name: "staged diff",
			configure: func(_ *testing.T, _ *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				return replaceDeploymentEntry,
					func(context.Context, string, []string) ([]byte, error) {
						return nil, errDeploymentCoverage
					}, writeGitTree
			},
		},
		{
			name: "write tree",
			configure: func(_ *testing.T, _ *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				return replaceDeploymentEntry, stagedTUIDiff,
					func(context.Context, string) (string, error) { return "", errDeploymentCoverage }
			},
		},
		{
			name: "tree content",
			configure: func(_ *testing.T, draft *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				return replaceDeploymentEntry, stagedTUIDiff,
					func(context.Context, string) (string, error) { return draft.base.tree, nil }
			},
		},
		{
			name: "confirmed content mismatch",
			configure: func(_ *testing.T, draft *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				draft.confirmation.content = draft.source.Content

				return replaceDeploymentEntry, stagedTUIDiff, writeGitTree
			},
		},
		{
			name: "confirmed diff mismatch",
			configure: func(_ *testing.T, draft *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				draft.confirmation.diff = append(draft.confirmation.diff, '\n')

				return replaceDeploymentEntry, stagedTUIDiff, writeGitTree
			},
		},
		{
			name: "confirmed tree mismatch",
			configure: func(_ *testing.T, draft *tuiDeploymentDraft) (
				func(string, string, []byte, []byte) (bool, error),
				func(context.Context, string, []string) ([]byte, error),
				func(context.Context, string) (string, error),
			) {
				draft.confirmation.expectedTree = draft.base.tree

				return replaceDeploymentEntry, stagedTUIDiff, writeGitTree
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			draft := newDeploymentDraftFixture(t)
			replace, diff, writeTree := test.configure(t, &draft)
			if _, _, err := stageDeploymentEditWith(
				t.Context(), draft, replace, diff, writeTree,
			); err == nil {
				t.Fatal("stageDeploymentEditWith() succeeded")
			}
			assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
			if _, err := cleanGitTree(t.Context(), draft.repository); err != nil {
				t.Fatalf("stage rescue left repository dirty: %v", err)
			}
		})
	}

	t.Run("git add", func(t *testing.T) {
		t.Parallel()

		draft := newDeploymentDraftFixture(t)
		lockPath := filepath.Join(draft.repository, ".git", "index.lock")
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatalf("WriteFile(index.lock) error = %v", err)
		}
		if _, _, err := stageDeploymentEdit(t.Context(), draft); err == nil {
			t.Fatal("stageDeploymentEdit(index lock) succeeded")
		}
		if err := os.Remove(lockPath); err != nil {
			t.Fatalf("Remove(index.lock) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
	})

	t.Run("attribute drift after publication", func(t *testing.T) {
		t.Parallel()

		draft := newDeploymentDraftFixture(t)
		path, err := absoluteGitPath(t.Context(), draft.repository, "info/attributes")
		if err != nil {
			t.Fatalf("absoluteGitPath(info/attributes) error = %v", err)
		}
		replace := func(repository, entry string, before, after []byte) (bool, error) {
			published, replaceErr := replaceDeploymentEntry(repository, entry, before, after)
			if replaceErr == nil {
				replaceErr = os.WriteFile(path, []byte("unrelated text\n"), 0o600)
			}
			if replaceErr != nil {
				return published, errors.Join(errDeploymentEditInvalid, replaceErr)
			}

			return published, nil
		}
		if _, _, err = stageDeploymentEditWith(
			t.Context(), draft, replace, stagedTUIDiff, writeGitTree,
		); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("stageDeploymentEditWith(attribute drift) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
		if _, err = cleanGitTree(t.Context(), draft.repository); err != nil {
			t.Fatalf("attribute drift rescue left repository dirty: %v", err)
		}
	})
}

func newDeploymentDraftFixture(t *testing.T) tuiDeploymentDraft {
	t.Helper()

	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), "2", false,
	); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if workspace.draft == nil {
		t.Fatal("Preview() did not retain a draft")
	}

	return *workspace.draft
}

//nolint:cyclop,funlen // Commit outcomes require a fresh staged Git proof for each case.
func TestDeploymentCommitSettlementBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("signed fallback", func(t *testing.T) {
		t.Parallel()

		workspace, _, _ := newStagedDeploymentWorkspace(t)
		result, err := workspace.commitWith(
			t.Context(), "Update api deployment", false,
			func(context.Context, tuiStagedProof, string, bool) error { return errDeploymentCoverage },
		)
		if err != nil || result.Outcome != tui.CommitNeedsUnsignedApproval {
			t.Fatalf("commitWith(signed fallback) = %#v, %v", result, err)
		}
	})

	for _, test := range []struct {
		name      string
		commitErr error
	}{
		{name: "unsigned commit error", commitErr: errDeploymentCoverage},
		{name: "unproven outcome"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace, _, _ := newStagedDeploymentWorkspace(t)
			result, err := workspace.commitWith(
				t.Context(), "Update api deployment", true,
				func(context.Context, tuiStagedProof, string, bool) error { return test.commitErr },
			)
			if !isDeploymentFailure(err, tui.DeploymentCommitFailed) ||
				result.Outcome != 0 {
				t.Fatalf("commitWith(%s) = %#v, %v", test.name, result, err)
			}
		})
	}

	t.Run("caller cancellation", func(t *testing.T) {
		t.Parallel()

		workspace, _, _ := newStagedDeploymentWorkspace(t)
		ctx, cancel := context.WithCancel(t.Context())
		result, err := workspace.commitWith(
			ctx, "Update api deployment", true,
			func(context.Context, tuiStagedProof, string, bool) error {
				cancel()

				return errDeploymentCoverage
			},
		)
		if !errors.Is(err, context.Canceled) || result.Outcome != 0 {
			t.Fatalf("commitWith(cancelled) = %#v, %v", result, err)
		}
	})

	for _, test := range []struct {
		name      string
		commitErr error
	}{
		{name: "failed with changed stage", commitErr: errDeploymentCoverage},
		{name: "unproven with changed stage"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace, _, _ := newStagedDeploymentWorkspace(t)
			staged := *workspace.staged
			staged.proof.expectedTree = strings.Repeat("a", 40)
			result, err := workspace.settleCommit(
				t.Context(), staged, "Update api deployment", true, test.commitErr,
			)
			if !isDeploymentFailure(err, tui.DeploymentWorktreeUnknown) || result.Outcome != 0 {
				t.Fatalf("settleCommit(%s) = %#v, %v", test.name, result, err)
			}
		})
	}

	t.Run("validation unavailable", func(t *testing.T) {
		t.Parallel()

		workspace, _, _ := newStagedDeploymentWorkspace(t)
		result, err := workspace.commitWith(
			t.Context(), "Update api deployment", true,
			func(ctx context.Context, proof tuiStagedProof, message string, unsigned bool) error {
				workspace.runtimeBase = testRelativePath

				return commitTUIStagedProof(ctx, proof, message, unsigned)
			},
		)
		if err != nil || result.Outcome != tui.CommitValidationUnavailable {
			t.Fatalf("commitWith(validation unavailable) = %#v, %v", result, err)
		}
	})

	t.Run("repository signer override", func(t *testing.T) {
		t.Parallel()

		workspace, _, repository := newStagedDeploymentWorkspace(t)
		if _, err := runGit(
			t.Context(), repository, "config", "--local", "gpg.ssh.program", "hostile-signer",
		); err != nil {
			t.Fatalf("git config error = %v", err)
		}
		result, err := workspace.commitWith(
			t.Context(), "Update api deployment", false,
			func(context.Context, tuiStagedProof, string, bool) error {
				t.Fatal("commit called after signer configuration failure")

				return nil
			},
		)
		if err != nil || result.Outcome != tui.CommitNeedsUnsignedApproval {
			t.Fatalf("commitWith(hostile signer) = %#v, %v", result, err)
		}
	})
}

func isDeploymentFailure(err error, code tui.DeploymentFailure) bool {
	action, valid := errors.AsType[*tui.DeploymentActionError](err)

	return valid && action.Code == code
}

func TestPublicDeploymentActionErrorClassification(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		err  error
		want tui.DeploymentFailure
	}{
		{errDeploymentWorktreeUnknown, tui.DeploymentWorktreeUnknown},
		{errDeploymentPublishFailed, tui.DeploymentPublishFailed},
		{errDeploymentCoverage, tui.DeploymentValidationFailed},
	} {
		err := publicDeploymentActionError(test.err, tui.DeploymentValidationFailed)
		if !isDeploymentFailure(err, test.want) || !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("publicDeploymentActionError(%v) = %v, want %q", test.err, err, test.want)
		}
	}
	for _, target := range []error{context.Canceled, context.DeadlineExceeded} {
		err := publicDeploymentActionError(target, tui.DeploymentCommitFailed)
		if !isDeploymentFailure(err, tui.DeploymentCommitFailed) || !errors.Is(err, target) {
			t.Fatalf("publicDeploymentActionError(%v) = %v", target, err)
		}
	}
}

func newStagedDeploymentWorkspace(
	t *testing.T,
) (*tuiDeploymentWorkspace, application.Request, string) {
	t.Helper()

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), "2", false,
	); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if workspace.draft != nil {
		t.Fatal("Stage() retained a second draft representation")
	}

	return workspace, request, repository
}

func TestDeploymentDiscardReportsRollbackFailure(t *testing.T) {
	t.Parallel()

	workspace, _, _ := newStagedDeploymentWorkspace(t)
	invalidHead := strings.Repeat("a", 40)
	workspace.staged.draft.base.head = invalidHead
	workspace.staged.proof.base.head = invalidHead
	err := workspace.Discard(t.Context())
	if !isDeploymentFailure(err, tui.DeploymentWorktreeUnknown) {
		t.Fatalf("Discard(mismatched base) error = %v", err)
	}
}

//nolint:cyclop,funlen // One repository timeline reaches each immutable restore rejection boundary.
func TestDeploymentRestoreRejectsInvalidHistoricalCandidates(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	valid := deploymentComposeFixture()
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatalf("WriteFile(initial) error = %v", err)
	}
	dataEntry := "deploy/data.txt"
	if err := os.WriteFile(filepath.Join(repository, filepath.FromSlash(dataEntry)), []byte("data\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(data) error = %v", err)
	}
	commitApplyTestRepository(t, repository, deploymentComposeEntry, dataEntry)

	invalidRevision := commitDeploymentContent(t, repository, []byte("services: [\n"), "invalid compose")
	unsupportedRevision := commitDeploymentContent(t, repository, deploymentUnsupportedFixture(), "unsupported compose")
	missingServiceRevision := commitDeploymentContent(t, repository, deploymentOtherServiceFixture(), "missing service")
	missingDependencyRevision := commitDeploymentContent(t, repository, []byte(
		"services:\n  api:\n    image: example.invalid/api:latest\n    env_file: missing.env\n",
	), "missing dependency")
	bindRevision := commitDeploymentContent(t, repository, []byte(
		"name: example\nservices:\n  api:\n    container_name: example-api\n"+
			"    image: example.invalid/api:latest\n    network_mode: bridge\n"+
			"    volumes:\n      - ./data.txt:/data:ro\n",
	), "bind tracked data")
	selfBindRevision := commitDeploymentContent(t, repository, []byte(
		"name: example\nservices:\n  api:\n    container_name: example-api\n"+
			"    image: example.invalid/api:latest\n    network_mode: bridge\n"+
			"    volumes:\n      - ./compose.yaml:/config:ro\n",
	), "self bind")
	if _, err := runGit(t.Context(), repository, "rm", "--quiet", "--", deploymentComposeEntry); err != nil {
		t.Fatalf("git rm error = %v", err)
	}
	deletedRevision := commitDeploymentIndex(t, repository, "delete compose")
	currentRevision := commitDeploymentContent(t, repository, valid, "restore current")

	runtimeBase := t.TempDir()
	environment := map[string]string{testComposeDisableEnvFile: trueValue}
	source, err := loadTrackedComposeSource(t.Context(), path, repository, environment, runtimeBase)
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
	request := application.Request{Source: source, Service: applyServiceValue}
	workspace := &tuiDeploymentWorkspace{environment: environment, runtimeBase: runtimeBase}
	for _, revision := range []string{
		invalidRevision,
		unsupportedRevision,
		missingServiceRevision,
		missingDependencyRevision,
		deletedRevision,
	} {
		if _, restoreErr := workspace.PreviewRestore(t.Context(), request, revision); !errors.Is(
			restoreErr, errDeploymentEditInvalid,
		) {
			t.Fatalf("PreviewRestore(%s) error = %v", revision, restoreErr)
		}
	}
	currentPreview, err := workspace.PreviewRestore(t.Context(), request, currentRevision)
	if err != nil || !currentPreview.NoChanges || workspace.draft != nil {
		t.Fatalf("PreviewRestore(current) = %#v, %v", currentPreview, err)
	}
	bindPreview, err := workspace.PreviewRestore(t.Context(), request, bindRevision)
	if err != nil || bindPreview.Restore != bindRevision {
		t.Fatalf("PreviewRestore(tracked bind) = %#v, %v", bindPreview, err)
	}
	workspace.draft = nil
	base, err := cleanGitTree(t.Context(), repository)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	treeEntry, found, err := readGitTreeEntry(t.Context(), repository, selfBindRevision+"^{tree}", deploymentComposeEntry)
	if err != nil || !found {
		t.Fatalf("readGitTreeEntry(self bind) = %#v, %t, %v", treeEntry, found, err)
	}
	selfBindContent, err := readGitBlob(t.Context(), repository, treeEntry.object)
	if err != nil {
		t.Fatalf("readGitBlob(self bind) error = %v", err)
	}
	candidate, err := captureDeploymentRestore(
		t.Context(), source, base.tree, selfBindContent, environment, runtimeBase,
	)
	if err != nil {
		t.Fatalf("captureDeploymentRestore(self bind) error = %v", err)
	}
	project, err := compose.Load(t.Context(), candidate)
	if err != nil {
		t.Fatalf("compose.Load(self bind) error = %v", err)
	}
	if _, err = project.ServiceSpec(applyServiceValue); err != nil {
		t.Fatalf("ServiceSpec(self bind) error = %v", err)
	}
	preview, err := workspace.PreviewRestore(t.Context(), request, selfBindRevision)
	if err != nil || preview.Restore != selfBindRevision {
		t.Fatalf("PreviewRestore(self bind) = %#v, %v", preview, err)
	}
	workspace.draft = nil

	branch, err := currentGitBranch(t.Context(), repository)
	if err != nil {
		t.Fatalf("currentGitBranch() error = %v", err)
	}
	scope, err := compose.NewRepositoryScope(repository, repository, branch)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	request.Repository, err = scope.Bind(deploymentComposeEntry, source.Repository.Digest)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if _, err = workspace.PreviewRestore(t.Context(), request, selfBindRevision); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("PreviewRestore(scope unavailable) error = %v", err)
	}

	commitDeploymentContent(t, repository, deploymentOtherServiceFixture(), "current service missing")
	currentSource, err := loadTrackedComposeSource(t.Context(), path, repository, environment, runtimeBase)
	if err != nil {
		t.Fatalf("loadTrackedComposeSource(missing current service) error = %v", err)
	}
	workspace.draft = nil
	request = application.Request{Source: currentSource, Service: applyServiceValue}
	if _, err = workspace.PreviewRestore(t.Context(), request, bindRevision); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("PreviewRestore(missing current service) error = %v", err)
	}
}

func commitDeploymentContent(t *testing.T, repository string, content []byte, message string) string {
	t.Helper()

	path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := runGit(t.Context(), repository, "add", "--", deploymentComposeEntry); err != nil {
		t.Fatalf("git add error = %v", err)
	}

	return commitDeploymentIndex(t, repository, message)
}

func commitDeploymentIndex(t *testing.T, repository string, message string) string {
	t.Helper()

	if _, err := runGit(
		t.Context(), repository,
		"-c", "user.name=Maniud Tests", "-c", "user.email=maniud@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", message,
	); err != nil {
		t.Fatalf("git commit error = %v", err)
	}
	revision, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve HEAD error = %v", err)
	}

	return revision
}

//nolint:cyclop,funlen // Subtests exercise each immutable committed-request readback boundary.
func TestCommittedDeploymentRequestRejectsInvalidReadback(t *testing.T) {
	t.Parallel()

	t.Run("capture", func(t *testing.T) {
		t.Parallel()

		workspace, _, _ := newStagedDeploymentWorkspace(t)
		staged := *workspace.staged
		staged.draft.entry = testDeploymentMissingEntry
		if _, err := workspace.committedRequest(t.Context(), staged, staged.draft.base.head); !errors.Is(
			err, errDeploymentEditInvalid,
		) {
			t.Fatalf("committedRequest(capture) error = %v", err)
		}
	})

	t.Run("runtime pin", func(t *testing.T) {
		t.Parallel()

		workspace, _, _ := newStagedDeploymentWorkspace(t)
		workspace.runtimeBase = testRelativePath
		staged := *workspace.staged
		if _, err := workspace.committedRequest(t.Context(), staged, staged.draft.base.head); !errors.Is(
			err, errDeploymentEditInvalid,
		) {
			t.Fatalf("committedRequest(runtime pin) error = %v", err)
		}
	})

	for _, test := range []struct {
		name    string
		content []byte
	}{
		{name: "capture", content: []byte("services: [\n")},
		{name: "compose load", content: deploymentUnsupportedFixture()},
		{name: testServiceName, content: deploymentOtherServiceFixture()},
		{name: "runtime path", content: []byte(
			"services:\n  api:\n    image: example.invalid/api:latest\n    volumes:\n      - ./missing:/data:ro\n",
		)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace, staged := stagedDeploymentCandidateFixture(t, test.content)
			if _, err := workspace.committedRequest(t.Context(), staged, staged.draft.base.head); !errors.Is(
				err, errDeploymentEditInvalid,
			) {
				t.Fatalf("committedRequest(%s) error = %v", test.name, err)
			}
		})
	}

	t.Run("tree state", func(t *testing.T) {
		t.Parallel()

		workspace, _, _ := newStagedDeploymentWorkspace(t)
		staged := *workspace.staged
		if _, err := workspace.committedRequest(t.Context(), staged, staged.draft.base.head); !errors.Is(
			err, errDeploymentEditInvalid,
		) {
			t.Fatalf("committedRequest(tree state) error = %v", err)
		}
	})

	t.Run("repository scope", func(t *testing.T) {
		t.Parallel()

		workspace, _, repository := newStagedDeploymentWorkspace(t)
		branch, err := currentGitBranch(t.Context(), repository)
		if err != nil {
			t.Fatalf("currentGitBranch() error = %v", err)
		}
		scope, err := compose.NewRepositoryScope(repository, repository, branch)
		if err != nil {
			t.Fatalf("NewRepositoryScope() error = %v", err)
		}
		workspace.staged.draft.scope = scope
		staged := *workspace.staged
		message := "Update api deployment"
		if err = commitTUIStagedProof(t.Context(), staged.proof, message, true); err != nil {
			t.Fatalf("commitTUIStagedProof() error = %v", err)
		}
		head, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
		if err != nil {
			t.Fatalf("resolve HEAD error = %v", err)
		}
		request, err := workspace.committedRequest(t.Context(), staged, head)
		if err != nil || request.Source.Repository == nil ||
			!request.Repository.ValidFor(request.Source.Repository.Digest) {
			t.Fatalf("committedRequest(scope) = %#v, %v", request, err)
		}
	})
}

func stagedDeploymentCandidateFixture(
	t *testing.T,
	content []byte,
) (*tuiDeploymentWorkspace, tuiStagedDeployment) {
	t.Helper()

	draft := newDeploymentDraftFixture(t)
	staged, _, err := stageDeploymentEdit(t.Context(), draft)
	if err != nil {
		t.Fatalf("stageDeploymentEdit() error = %v", err)
	}
	path := filepath.Join(draft.repository, filepath.FromSlash(draft.entry))
	if err = os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(invalid candidate) error = %v", err)
	}
	if _, err = runGit(t.Context(), draft.repository, "add", "--", draft.entry); err != nil {
		t.Fatalf("git add invalid candidate error = %v", err)
	}
	staged.proof.expectedTree, err = writeGitTree(t.Context(), draft.repository)
	if err != nil {
		t.Fatalf("writeGitTree(invalid candidate) error = %v", err)
	}
	staged.content = bytes.Clone(content)
	if _, err = runGit(t.Context(), draft.repository, "reset", "--hard", "--quiet", "HEAD"); err != nil {
		t.Fatalf("git reset invalid candidate error = %v", err)
	}

	return &tuiDeploymentWorkspace{
		environment: map[string]string{testComposeDisableEnvFile: trueValue}, runtimeBase: t.TempDir(),
	}, staged
}

func TestCaptureDeploymentRestoreRejectsMalformedProof(t *testing.T) {
	t.Parallel()

	if _, err := captureDeploymentRestore(
		t.Context(), compose.Source{}, strings.Repeat("a", 40), nil, nil, t.TempDir(),
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("captureDeploymentRestore(no repository) error = %v", err)
	}
	source := compose.Source{Repository: &compose.RepositorySnapshot{
		Root: t.TempDir(), Entry: composeFileValue, Files: map[string]compose.RepositoryFile{},
	}}
	if _, err := captureDeploymentRestore(
		t.Context(), source, strings.Repeat("a", 40), nil, nil, t.TempDir(),
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("captureDeploymentRestore(missing entry) error = %v", err)
	}
	if _, err := captureDeploymentRestore(t.Context(), source, "bad", nil, nil, t.TempDir()); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("captureDeploymentRestore(invalid tree) error = %v", err)
	}
	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	state, err := cleanGitTree(t.Context(), repository)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	if _, err = captureDeploymentRestore(
		t.Context(), request.Source, state.tree, request.Source.Content,
		workspace.environment, testRelativePath,
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("captureDeploymentRestore(relative runtime) error = %v", err)
	}
}

func TestDeploymentCommitAndDiscardRejectInvalidProof(t *testing.T) {
	t.Parallel()

	workspace := &tuiDeploymentWorkspace{}
	if _, err := workspace.Commit(t.Context(), "Update api deployment", true); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("Commit(without stage) error = %v", err)
	}
	workspace.staged = &tuiStagedDeployment{}
	if _, err := workspace.Commit(t.Context(), "", true); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Commit(invalid message) error = %v", err)
	}
	if _, err := workspace.Commit(t.Context(), "Update api deployment", true); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("Commit(invalid proof) error = %v", err)
	}
	if err := workspace.Discard(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Discard(invalid proof) error = %v", err)
	}
}
