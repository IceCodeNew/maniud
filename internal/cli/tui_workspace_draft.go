package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/mattn/go-shellwords"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
	"github.com/IceCodeNew/maniud/internal/tui"
)

//nolint:cyclop // Preview keeps repository proof, generation, and draft recovery in one ordered transaction.
func prepareTUIServiceDraft(
	ctx context.Context,
	input string,
	workspace *tuiServiceWorkspace,
) (tuiServiceDraft, error) {
	registration, repository, base, err := registeredTUIWorkspace(ctx, workspace.registrationPath)
	if err != nil {
		return tuiServiceDraft{}, err
	}
	repositoryScope, err := gitOpsRepositoryScope(ctx, registration, repository)
	if err != nil {
		return tuiServiceDraft{}, err
	}
	arguments, err := tuiGenInvocation(input, repository)
	if err != nil {
		return tuiServiceDraft{}, err
	}
	dependencies, err := workspace.dependencies(
		workspace.environment,
		io.Discard,
		func() (string, error) { return repository, nil },
		workspace.runtimes,
	)
	if err != nil {
		return tuiServiceDraft{}, err
	}
	generated, err := workspace.render(ctx, arguments, dependencies)
	if err != nil || !validGeneratedTUIService(generated) {
		return tuiServiceDraft{}, errors.Join(err, runtimeargv.ErrInvalid)
	}
	draft := tuiServiceDraft{
		generated: generated, repository: repository, repositoryScope: repositoryScope,
		branch: registration.Branch, base: base,
	}
	if err = recoverPartialTUIDraftPublication(ctx, repository, generated); err != nil {
		return tuiServiceDraft{}, err
	}
	state, err := gitTreeAllowingTUIDrafts(ctx, repository)
	if err != nil || state != base {
		return tuiServiceDraft{}, compose.ErrInvalidSource
	}
	if verifyTUIDraftState(ctx, draft) {
		draft.recovered = true

		return draft, nil
	}
	if err = writeTUIDraftFiles(generated); err != nil {
		return tuiServiceDraft{}, err
	}
	if !verifyTUIDraftState(context.WithoutCancel(ctx), draft) {
		return tuiServiceDraft{}, compose.ErrInvalidSource
	}

	return draft, nil
}

func registeredTUIWorkspace(
	ctx context.Context,
	registrationPath string,
) (gitOpsRegistration, string, gitTreeState, error) {
	registration, err := readGitOpsRegistration(registrationPath)
	if err != nil {
		return gitOpsRegistration{}, "", gitTreeState{}, compose.ErrInvalidSource
	}
	repository, err := resolveGitOpsRepository(ctx, registration.Repository)
	if err != nil {
		return gitOpsRegistration{}, "", gitTreeState{}, err
	}
	branch, err := currentGitBranch(ctx, repository)
	if err != nil || branch != registration.Branch {
		return gitOpsRegistration{}, "", gitTreeState{}, errGitOpsRepositoryInvalid
	}
	head, err := resolveGitObject(ctx, repository, "HEAD^{commit}")
	if err != nil {
		return gitOpsRegistration{}, "", gitTreeState{}, errGitOpsRepositoryInvalid
	}
	tree, err := resolveGitObject(ctx, repository, "HEAD^{tree}")
	if err != nil {
		return gitOpsRegistration{}, "", gitTreeState{}, errGitOpsRepositoryInvalid
	}

	return registration, repository, gitTreeState{head: head, tree: tree}, nil
}

func validGeneratedTUIService(generated generatedCompose) bool {
	return generated.service != "" && generated.image != "" && generated.runtime != "" &&
		validTUIArtifactPath(generated.path, generated.service) &&
		(generated.preparationPath == "" || validTUIPreparationPath(generated))
}

func tuiGenInvocation(input, workingDirectory string) (genInvocation, error) {
	invocation, err := parseTUIServiceInput(input)
	if err != nil {
		return genInvocation{}, err
	}
	projection, err := parseGenProjection(invocation, workingDirectory)
	if err != nil {
		return genInvocation{}, err
	}
	invocation.output = filepath.Join(gitOpsServicesDirectory, projection.Name()+".yaml")

	return invocation, nil
}

func parseTUIServiceInput(input string) (genInvocation, error) {
	arguments, err := tokenizeTUIServiceInput(input)
	if err != nil {
		return genInvocation{}, err
	}

	invocation := genInvocation{}
	switch {
	case len(arguments) == 1 && strings.Contains(arguments[0], "://"):
		invocation.source = arguments[0]
	case tuiRuntimeCommand(arguments):
		invocation.runtimeArgs = arguments
	default:
		return genInvocation{}, runtimeargv.ErrInvalid
	}

	return invocation, nil
}

func tokenizeTUIServiceInput(input string) ([]string, error) {
	if input == "" || len(input) > maximumTUIServiceInput || strings.ContainsAny(input, "\x00\r\n") {
		return nil, runtimeargv.ErrInvalid
	}
	parser := shellwords.NewParser()
	arguments, err := parser.Parse(input)
	if err != nil || len(arguments) == 0 || parser.Position >= 0 {
		return nil, runtimeargv.ErrInvalid
	}

	return arguments, nil
}

func tuiRuntimeCommand(arguments []string) bool {
	if len(arguments) < minimumRuntimeArguments {
		return false
	}
	switch arguments[0] {
	case "docker", "podman", nerdctlRuntimeCommand:
		return arguments[1] == "create" || arguments[1] == "run"
	default:
		return false
	}
}

func validTUIArtifactPath(path, service string) bool {
	return path == filepath.Join(gitOpsServicesDirectory, service+".yaml")
}

func validTUIPreparationPath(generated generatedCompose) bool {
	want := strings.TrimSuffix(generated.path, filepath.Ext(generated.path)) + ".prepare.sh"

	return generated.preparationPath == want
}

func publicServiceDraft(draft tuiServiceDraft) tui.ServiceDraft {
	return tui.ServiceDraft{
		Runtime:      draft.generated.runtime.String(),
		Image:        draft.generated.image,
		Service:      draft.generated.service,
		ComposePath:  filepath.ToSlash(draft.generated.path),
		Preparation:  filepath.ToSlash(draft.generated.preparationPath),
		WarningCount: len(draft.generated.warnings),
		Recovered:    draft.recovered,
	}
}
