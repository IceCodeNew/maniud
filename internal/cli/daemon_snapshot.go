package cli

import (
	"context"
	"errors"
	"io"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
)

type gitOpsServiceSnapshot struct {
	arguments    applyInvocation
	dependencies applyDependencies
	plan         application.Plan
}

func reconcileGitOpsSnapshot(
	ctx context.Context,
	root string,
	selectedCommit string,
	output io.Writer,
	dependencies applyDependencies,
) error {
	services, err := captureGitOpsSnapshot(ctx, root, selectedCommit, dependencies)
	if err != nil {
		return err
	}

	for _, service := range services {
		if err = executeGitOpsMutation(ctx, service, output); err != nil {
			return err
		}
	}

	return nil
}

func recoverGitOpsSnapshot(
	ctx context.Context,
	root string,
	selectedCommit string,
	output io.Writer,
	dependencies applyDependencies,
) error {
	services, err := captureGitOpsSnapshot(ctx, root, selectedCommit, dependencies)
	if err != nil {
		return err
	}

	return recoverGitOpsServices(ctx, services, output)
}

func recoverGitOpsServices(
	ctx context.Context,
	services []gitOpsServiceSnapshot,
	output io.Writer,
) error {
	for _, service := range services {
		if !gitOpsRecoveryPlan(service.plan.Kind) {
			continue
		}
		if err := executeGitOpsMutation(ctx, service, output); err != nil {
			return err
		}
	}

	return nil
}

func executeGitOpsMutation(
	ctx context.Context,
	service gitOpsServiceSnapshot,
	output io.Writer,
) error {
	request, err := loadApplyRequest(ctx, service.arguments, service.dependencies)
	if err != nil {
		return err
	}
	plan, err := service.dependencies.operations.Apply(ctx, request)
	if err != nil {
		publishCLIEvent(service.dependencies.events, application.Event{
			Kind:    application.EventGitOpsServiceApplyFailed,
			Plan:    service.plan.Kind,
			Project: service.plan.Project,
			Service: service.plan.Service,
			Runtime: service.plan.Runtime,
		})

		return errors.Join(err)
	}

	return writeApplyPlan(output, plan, false, service.arguments.json)
}

func gitOpsRecoveryPlan(kind application.PlanKind) bool {
	return kind == application.PlanResume ||
		kind == application.PlanProbeUnknownEffect ||
		kind == application.PlanRestore
}

func captureGitOpsSnapshot(
	ctx context.Context,
	root string,
	selectedCommit string,
	dependencies applyDependencies,
) ([]gitOpsServiceSnapshot, error) {
	state, err := cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return nil, errGitOpsRepositoryInvalid
	}

	paths, err := listGitOpsServiceFiles(root)
	if err != nil {
		return nil, err
	}

	services := make([]gitOpsServiceSnapshot, 0, len(paths))
	for _, path := range paths {
		source, loadErr := dependencies.loadSource(ctx, path)
		if loadErr != nil {
			return nil, loadErr
		}
		arguments := applyInvocation{compose: path, json: true}
		pinned := dependenciesWithApplySource(dependencies, path, source)
		plan, prepareErr := prepareApplyPlan(ctx, arguments, pinned)
		if prepareErr != nil {
			return nil, prepareErr
		}
		services = append(services, gitOpsServiceSnapshot{
			arguments: arguments, dependencies: pinned, plan: plan,
		})
	}

	state, err = cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return nil, errGitOpsRepositoryInvalid
	}

	return services, nil
}

func dependenciesWithApplySource(
	dependencies applyDependencies,
	path string,
	source compose.Source,
) applyDependencies {
	dependencies.loadSource = func(_ context.Context, requested string) (compose.Source, error) {
		if requested != path {
			return compose.Source{}, compose.ErrInvalidSource
		}

		return source, nil
	}

	return dependencies
}
