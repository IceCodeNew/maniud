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

var errGitOpsRecoverySourceBlocked = errors.New(string(application.EventReasonRecoverySourceBlocked))

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

func reconcileGitOpsSnapshotResult(
	ctx context.Context,
	root string,
	selectedCommit string,
	output io.Writer,
	dependencies applyDependencies,
) (gitOpsCycleCounts, error) {
	snapshot, err := prepareGitOpsSnapshot(ctx, root, selectedCommit, dependencies)
	if err != nil {
		return gitOpsCycleCounts{}, err
	}

	counts := gitOpsCycleCounts{
		skipped: len(snapshot.skipped), skippedSources: snapshot.skipped,
		sourceBlockersObserved: true,
	}
	for index, service := range snapshot.services {
		if err = executeGitOpsMutation(ctx, service, output); err != nil {
			counts.failed = 1
			counts.deferred = len(snapshot.services) - index - 1

			return counts, err
		}
		if service.plan.Kind == application.PlanUnchanged {
			counts.unchanged++
		} else {
			counts.applied++
		}
	}

	return counts, nil
}

//nolint:cyclop // Recovery keeps inventory, source capture, and final checkout proof in one ordered transaction.
func recoverGitOpsSnapshotResult(
	ctx context.Context,
	root string,
	selectedCommit string,
	output io.Writer,
	dependencies applyDependencies,
) (gitOpsCycleCounts, error) {
	if dependencies.operations == nil || !dependencies.repository.Valid() ||
		dependencies.repositoryRoot != root {
		return gitOpsCycleCounts{}, errGitOpsRepositoryInvalid
	}
	if err := verifyGitOpsCheckout(ctx, root, selectedCommit); err != nil {
		return gitOpsCycleCounts{}, errGitOpsRepositoryInvalid
	}
	inventory, err := dependencies.operations.RepositoryInventory(ctx, dependencies.repository)
	if err != nil {
		return gitOpsCycleCounts{}, fmt.Errorf("read repository recovery inventory: %w", err)
	}
	if len(inventory) == 0 {
		return gitOpsCycleCounts{recoveryBlockerObserved: true}, nil
	}
	sources, err := captureGitOpsSources(ctx, root, selectedCommit, dependencies)
	if err != nil {
		return gitOpsCycleCounts{}, err
	}
	recoveries, err := prepareRepositoryRecoveries(ctx, sources, inventory, dependencies)
	if err != nil {
		if errors.Is(err, errGitOpsRecoverySourceBlocked) {
			return gitOpsCycleCounts{recoveryBlockerObserved: true}, err
		}

		return gitOpsCycleCounts{}, err
	}
	if err = verifyGitOpsCheckout(ctx, root, selectedCommit); err != nil {
		return gitOpsCycleCounts{}, errGitOpsRepositoryInvalid
	}

	counts, err := recoverGitOpsServicesResult(ctx, recoveries, output)
	counts.recoveryBlockerObserved = true

	return counts, err
}

func verifyGitOpsCheckout(ctx context.Context, root string, selectedCommit string) error {
	state, err := cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return errGitOpsRepositoryInvalid
	}

	return nil
}

func prepareRepositoryRecoveries(
	ctx context.Context,
	sources []gitOpsSourceSnapshot,
	inventory []application.RepositoryTransaction,
	dependencies applyDependencies,
) ([]gitOpsServiceSnapshot, error) {
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
		sourceRecoveries, err := prepareRepositorySourceRecoveries(
			ctx, source, transactions, dependencies,
		)
		if err != nil {
			return nil, err
		}
		recoveries = append(recoveries, sourceRecoveries...)
	}
	for location := range required {
		if _, found := seen[location]; !found {
			return nil, errGitOpsRecoverySourceBlocked
		}
	}

	return recoveries, nil
}

func prepareRepositorySourceRecoveries(
	ctx context.Context,
	source gitOpsSourceSnapshot,
	transactions []application.RepositoryTransaction,
	dependencies applyDependencies,
) ([]gitOpsServiceSnapshot, error) {
	if source.blocked || !sourceMatchesRepositoryInventory(source, transactions) {
		return nil, errGitOpsRecoverySourceBlocked
	}
	services, err := prepareGitOpsSource(ctx, source, dependencies)
	if err != nil {
		if gitOpsSourceBlocker(err) {
			return nil, errGitOpsRecoverySourceBlocked
		}

		return nil, err
	}
	recoveries := make([]gitOpsServiceSnapshot, 0, len(transactions))
	for _, service := range services {
		if gitOpsRecoveryPlan(service.plan.Kind) {
			recoveries = append(recoveries, service)
		}
	}
	if len(recoveries) != len(transactions) {
		return nil, errGitOpsRecoverySourceBlocked
	}

	return recoveries, nil
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

func recoverGitOpsServicesResult(
	ctx context.Context,
	services []gitOpsServiceSnapshot,
	output io.Writer,
) (gitOpsCycleCounts, error) {
	counts := gitOpsCycleCounts{}
	for index, service := range services {
		if !gitOpsRecoveryPlan(service.plan.Kind) {
			continue
		}
		if err := executeGitOpsMutation(ctx, service, output); err != nil {
			counts.failed = 1
			counts.deferred = len(services) - index - 1

			return counts, err
		}
		counts.applied++
	}

	return counts, nil
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

func prepareGitOpsSnapshot(
	ctx context.Context,
	root string,
	selectedCommit string,
	dependencies applyDependencies,
) (gitOpsPreparedSnapshot, error) {
	sources, err := captureGitOpsSources(ctx, root, selectedCommit, dependencies)
	if err != nil {
		return gitOpsPreparedSnapshot{}, err
	}

	services := make([]gitOpsServiceSnapshot, 0, len(sources))
	skipped := make([]gitOpsSkippedSource, 0, len(sources))
	for _, source := range sources {
		if source.blocked {
			skipped = append(skipped, skippedGitOpsSource(root, source.path))

			continue
		}
		prepared, prepareErr := prepareGitOpsSource(ctx, source, dependencies)
		if gitOpsSourceBlocker(prepareErr) {
			skipped = append(skipped, skippedGitOpsSource(root, source.path))

			continue
		}
		if prepareErr != nil {
			return gitOpsPreparedSnapshot{}, prepareErr
		}
		services = append(services, prepared...)
	}
	state, err := cleanGitTree(ctx, root)
	if err != nil || state.head != selectedCommit {
		return gitOpsPreparedSnapshot{}, errGitOpsRepositoryInvalid
	}

	return gitOpsPreparedSnapshot{services: services, skipped: skipped}, nil
}

func captureGitOpsSources(
	ctx context.Context,
	root string,
	selectedCommit string,
	dependencies applyDependencies,
) ([]gitOpsSourceSnapshot, error) {
	return captureGitOpsSourcesWithLocation(ctx, root, selectedCommit, dependencies, gitOpsSourceLocation)
}

func captureGitOpsSourcesWithLocation(
	ctx context.Context,
	root string,
	selectedCommit string,
	dependencies applyDependencies,
	locationFor func(string, string, compose.RepositoryScope) (domain.Digest, error),
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
		location, locationErr := locationFor(root, path, dependencies.repository)
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
