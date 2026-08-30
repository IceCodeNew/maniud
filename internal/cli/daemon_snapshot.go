package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

var errGitOpsRecoverySourceBlocked = errors.New("recovery_source_blocked")

type gitOpsServiceSnapshot struct {
	arguments    applyInvocation
	dependencies applyDependencies
	plan         application.Plan
}

type gitOpsSourceSnapshot struct {
	path     string
	source   compose.Source
	services []string
	location domain.Digest
	blocked  bool
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
	if dependencies.operations == nil || !dependencies.repository.Valid() ||
		dependencies.repositoryRoot != root {
		return errGitOpsRepositoryInvalid
	}
	state, err := cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return errGitOpsRepositoryInvalid
	}
	inventory, err := dependencies.operations.RepositoryInventory(ctx, dependencies.repository)
	if err != nil {
		return fmt.Errorf("read repository recovery inventory: %w", err)
	}
	if len(inventory) == 0 {
		return nil
	}
	sources, err := captureGitOpsSources(ctx, root, selectedCommit, dependencies)
	if err != nil {
		return err
	}

	required := make(map[domain.Digest][]application.RepositoryTransaction)
	for _, transaction := range inventory {
		required[transaction.Location] = append(required[transaction.Location], transaction)
	}
	seen := make(map[domain.Digest]struct{}, len(sources))
	recoveries := make([]gitOpsServiceSnapshot, 0, len(inventory))
	for _, source := range sources {
		transactions := required[source.location]
		seen[source.location] = struct{}{}
		if len(transactions) == 0 {
			continue
		}
		if source.blocked || !sourceMatchesRepositoryInventory(source, transactions) {
			return errGitOpsRecoverySourceBlocked
		}
		services, prepareErr := prepareGitOpsSource(ctx, source, dependencies)
		if prepareErr != nil {
			if gitOpsSourceBlocker(prepareErr) {
				return errGitOpsRecoverySourceBlocked
			}

			return prepareErr
		}
		sourceRecoveries := 0
		for _, service := range services {
			if gitOpsRecoveryPlan(service.plan.Kind) {
				recoveries = append(recoveries, service)
				sourceRecoveries++
			}
		}
		if sourceRecoveries != len(transactions) {
			return errGitOpsRecoverySourceBlocked
		}
	}
	for location := range required {
		if _, found := seen[location]; !found {
			return errGitOpsRecoverySourceBlocked
		}
	}
	state, err = cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return errGitOpsRepositoryInvalid
	}

	return recoverGitOpsServices(ctx, recoveries, output)
}

func sourceMatchesRepositoryInventory(
	source gitOpsSourceSnapshot,
	transactions []application.RepositoryTransaction,
) bool {
	if source.source.Repository == nil {
		return false
	}
	for _, transaction := range transactions {
		if transaction.Location != source.location ||
			transaction.Source != source.source.Repository.Digest {
			return false
		}
	}

	return true
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
	sources, err := captureGitOpsSources(ctx, root, selectedCommit, dependencies)
	if err != nil {
		return nil, err
	}

	services := make([]gitOpsServiceSnapshot, 0, len(sources))
	for _, source := range sources {
		if source.blocked {
			continue
		}
		prepared, prepareErr := prepareGitOpsSource(ctx, source, dependencies)
		if gitOpsSourceBlocker(prepareErr) {
			continue
		}
		if prepareErr != nil {
			return nil, prepareErr
		}
		services = append(services, prepared...)
	}
	state, err := cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return nil, errGitOpsRepositoryInvalid
	}

	return services, nil
}

func captureGitOpsSources(
	ctx context.Context,
	root string,
	selectedCommit string,
	dependencies applyDependencies,
) ([]gitOpsSourceSnapshot, error) {
	state, err := cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return nil, errGitOpsRepositoryInvalid
	}

	paths, err := listGitOpsServiceFiles(root)
	if err != nil {
		return nil, err
	}

	sources := make([]gitOpsSourceSnapshot, 0, len(paths))
	for _, path := range paths {
		location, locationErr := gitOpsSourceLocation(root, path, dependencies.repository)
		if locationErr != nil {
			return nil, locationErr
		}
		source, captureErr := captureGitOpsSource(ctx, path, location, dependencies)
		if captureErr != nil {
			return nil, captureErr
		}
		sources = append(sources, source)
	}

	state, err = cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return nil, errGitOpsRepositoryInvalid
	}

	return sources, nil
}

func captureGitOpsSource(
	ctx context.Context,
	path string,
	location domain.Digest,
	dependencies applyDependencies,
) (gitOpsSourceSnapshot, error) {
	source, err := dependencies.loadSource(ctx, path)
	if err != nil {
		if gitOpsSourceBlocker(err) {
			return gitOpsSourceSnapshot{path: path, location: location, blocked: true}, nil
		}

		return gitOpsSourceSnapshot{}, fmt.Errorf("load gitops source: %w", err)
	}
	project, err := compose.Load(ctx, source)
	if err != nil {
		if gitOpsSourceBlocker(err) {
			return gitOpsSourceSnapshot{path: path, location: location, blocked: true}, nil
		}

		return gitOpsSourceSnapshot{}, fmt.Errorf("validate gitops source: %w", err)
	}
	names := project.ServiceNames()

	return gitOpsSourceSnapshot{
		path: path, source: source, services: names, location: location,
		blocked: len(names) == 0,
	}, nil
}

func gitOpsSourceBlocker(err error) bool {
	return errors.Is(err, compose.ErrInvalidSource) || errors.Is(err, compose.ErrExternalSource)
}

func gitOpsSourceLocation(
	root string,
	path string,
	scope compose.RepositoryScope,
) (domain.Digest, error) {
	if !scope.Valid() {
		return domain.Digest{}, nil
	}
	entry, err := filepath.Rel(root, path)
	if err != nil {
		return domain.Digest{}, errGitOpsRepositoryInvalid
	}
	location, err := scope.Location(filepath.ToSlash(entry))
	if err != nil {
		return domain.Digest{}, errGitOpsRepositoryInvalid
	}

	return location, nil
}

func prepareGitOpsSource(
	ctx context.Context,
	source gitOpsSourceSnapshot,
	dependencies applyDependencies,
) ([]gitOpsServiceSnapshot, error) {
	services := make([]gitOpsServiceSnapshot, 0, len(source.services))
	pinned := dependenciesWithApplySource(dependencies, source.path, source.source)
	for _, name := range source.services {
		arguments := applyInvocation{compose: source.path, service: name, json: true}
		plan, err := prepareApplyPlan(ctx, arguments, pinned)
		if err != nil {
			return nil, err
		}
		services = append(services, gitOpsServiceSnapshot{
			arguments: arguments, dependencies: pinned, plan: plan,
		})
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
