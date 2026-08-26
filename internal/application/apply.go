// Package application owns workload planning and transaction orchestration.
package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/store"
)

var (
	// ErrInvalidRequest reports an apply input outside the supported contract.
	ErrInvalidRequest = errors.New("apply request is invalid")
	// ErrConflictingState reports runtime or durable evidence that cannot be
	// reconciled without an operator-visible conflict.
	ErrConflictingState = errors.New("apply evidence conflicts")
	// ErrArchiveImageMissing reports a generated archive image that the operator
	// has not imported into the selected runtime.
	ErrArchiveImageMissing = errors.New("docker archive image is not imported")
)

// ImageResolver proves an immutable image identity for one runtime platform.
type ImageResolver interface {
	Resolve(
		ctx context.Context,
		source imageref.Source,
		platform domain.Platform,
	) (domain.ImageIdentity, error)
}

// Runtime provides read-only capability and workload evidence. Implementations
// must not perform image pulls or workload effects from these methods.
type Runtime interface {
	Inspect(ctx context.Context) (RuntimeEvidence, error)
	CheckWorkload(workload domain.DesiredWorkload) error
	ObserveWorkload(ctx context.Context, workload domain.DesiredWorkload) (WorkloadObservation, error)
}

// TransactionReader provides the durable evidence needed to distinguish a new
// deployment from recovery. Its methods must not migrate or mutate state.
type TransactionReader interface {
	AppliedService(
		ctx context.Context,
		project string,
		service string,
	) (store.AppliedService, bool, error)
	UnresolvedTransaction(
		ctx context.Context,
		project string,
		service string,
	) (store.Transaction, bool, error)
	Actions(ctx context.Context, identifier store.TransactionID) ([]store.Action, error)
}

// Service prepares apply operations from desired, runtime, image, and journal
// evidence. Preparation is read-only and is shared by dry-run and mutation.
type Service struct {
	images       ImageResolver
	runtime      Runtime
	transactions TransactionReader
	events       EventSink
}

// NewService creates an apply service over explicit read-only dependencies.
func NewService(images ImageResolver, runtime Runtime, transactions TransactionReader) *Service {
	return NewObservedService(images, runtime, transactions, nil)
}

// NewObservedService creates an apply service that attempts transient event
// publication after application-owned successful seams.
func NewObservedService(
	images ImageResolver,
	runtime Runtime,
	transactions TransactionReader,
	events EventSink,
) *Service {
	return &Service{images: images, runtime: runtime, transactions: transactions, events: events}
}

// Request selects one service from an already bounded in-memory Compose source.
type Request struct {
	Source  compose.Source
	Service string
}

// Prepare validates and classifies one apply without a runtime effect or
// durable write.
func (service *Service) Prepare(ctx context.Context, request Request) (Preparation, error) {
	var empty Preparation

	if service == nil || service.images == nil || service.runtime == nil || service.transactions == nil {
		return empty, ErrInvalidRequest
	}

	err := ctx.Err()
	if err != nil {
		return empty, fmt.Errorf("prepare apply: %w", err)
	}

	desired, err := service.prepareDesired(ctx, request)
	if err != nil {
		return empty, err
	}

	return service.prepareObserved(ctx, desired)
}

// DryRun returns the operator-facing classification while discarding private
// execution details retained by Prepare for a later mutation.
func (service *Service) DryRun(ctx context.Context, request Request) (Plan, error) {
	preparation, err := service.Prepare(ctx, request)
	if err != nil {
		return Plan{}, err
	}
	service.publishPlan(preparation)

	return preparation.Plan, nil
}

func (service *Service) publishPlan(preparation Preparation) {
	tryPublish(service.events, Event{
		Kind:    EventPlanPrepared,
		Plan:    preparation.Plan.Kind,
		Project: preparation.Plan.Project,
		Service: preparation.Plan.Service,
		Runtime: preparation.Plan.Runtime,
	})
}

type desiredApply struct {
	project   string
	image     domain.ImageIdentity
	workload  domain.DesiredWorkload
	execution RuntimeEvidence
}

func (service *Service) prepareDesired(ctx context.Context, request Request) (desiredApply, error) {
	var empty desiredApply

	project, err := compose.Load(ctx, request.Source)
	if err != nil {
		return empty, fmt.Errorf("prepare apply source: %w", err)
	}

	imageInput, err := project.ImageInput(request.Service)
	if err != nil {
		return empty, fmt.Errorf("prepare apply service: %w", err)
	}

	execution, err := service.runtime.Inspect(ctx)
	if err != nil {
		return empty, fmt.Errorf("prepare apply runtime: %w", err)
	}

	if !runtimeMatchesProject(project, request.Service, execution) {
		return empty, ErrInvalidRequest
	}

	image, err := service.resolveDesiredImage(ctx, imageInput, execution.Platform)
	if err != nil {
		return empty, fmt.Errorf("prepare apply image: %w", err)
	}

	workload, err := project.Workload(request.Service, image)
	if err != nil {
		return empty, fmt.Errorf("prepare apply workload: %w", err)
	}

	err = service.runtime.CheckWorkload(workload)
	if err != nil {
		return empty, fmt.Errorf("prepare apply capability: %w", err)
	}

	return desiredApply{
		project:   project.Name(),
		image:     image,
		workload:  workload,
		execution: execution,
	}, nil
}

func runtimeMatchesProject(project compose.Project, service string, execution RuntimeEvidence) bool {
	runtimeKind, err := project.Runtime(service)

	return err == nil && runtimeKind.SupportsWorkloads() && validRuntimeEvidence(execution) &&
		execution.Kind == runtimeKind
}

func (service *Service) resolveDesiredImage(
	ctx context.Context,
	input compose.ImageInput,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	switch input.Kind() {
	case compose.ImageInputRegistry:
		source, _ := input.RegistrySource()

		image, err := service.images.Resolve(ctx, source, platform)
		if err != nil {
			return domain.ImageIdentity{}, fmt.Errorf("resolve registry image: %w", err)
		}

		return image, nil
	case compose.ImageInputDockerArchive:
		identity, _ := input.ArchiveIdentity()
		if identity.Platform != platform {
			return domain.ImageIdentity{}, ErrInvalidRequest
		}

		return service.proveArchiveImage(ctx, identity)
	default:
		return domain.ImageIdentity{}, ErrInvalidRequest
	}
}

func (service *Service) proveArchiveImage(
	ctx context.Context,
	identity domain.ImageIdentity,
) (domain.ImageIdentity, error) {
	runtime, valid := service.runtime.(interface {
		ProbeImage(ctx context.Context, expected domain.ImageIdentity) (ImageProbe, error)
	})
	if !valid {
		return domain.ImageIdentity{}, ErrInvalidRequest
	}
	probe, err := runtime.ProbeImage(ctx, identity)
	if err != nil {
		return domain.ImageIdentity{}, fmt.Errorf("prove imported Docker archive image: %w", err)
	}

	switch probe.State {
	case ImageProbeMissing:
		if probe.Image != emptyImageEvidence() {
			return domain.ImageIdentity{}, ErrConflictingState
		}

		return domain.ImageIdentity{}, ErrArchiveImageMissing
	case ImageProbeObserved:
		if !probe.Matches(identity) {
			return domain.ImageIdentity{}, ErrConflictingState
		}

		return identity, nil
	case ImageProbeUnknown:
		return domain.ImageIdentity{}, ErrConflictingState
	default:
		return domain.ImageIdentity{}, ErrConflictingState
	}
}

func (service *Service) prepareObserved(ctx context.Context, desired desiredApply) (Preparation, error) {
	var empty Preparation

	transaction, found, err := service.transactions.UnresolvedTransaction(
		ctx,
		desired.project,
		desired.workload.ServiceName,
	)
	if err != nil {
		return empty, fmt.Errorf("prepare apply journal: %w", err)
	}

	applied, hasApplied, err := service.transactions.AppliedService(
		ctx,
		desired.project,
		desired.workload.ServiceName,
	)
	if err != nil {
		return empty, fmt.Errorf("prepare applied service: %w", err)
	}

	observation, err := service.runtime.ObserveWorkload(ctx, desired.workload)
	if err != nil {
		return empty, fmt.Errorf("prepare apply observation: %w", err)
	}

	if !validWorkloadObservation(observation, desired.workload) {
		return empty, ErrConflictingState
	}

	preparation := prepareApplyEvidence(desired, observation, transaction, found, applied, hasApplied)

	if found {
		preparation, err = service.prepareRecovery(ctx, preparation)
		if err != nil {
			return empty, err
		}
		preparation.Plan.Warnings = mountProbeFallbackWarnings(preparation)

		return preparation, nil
	}

	preparation.Plan.Kind, err = classifyNewApply(
		observation,
		desired.workload,
		desired.execution,
		applied,
		hasApplied,
	)
	if err != nil {
		return empty, err
	}
	preparation.Plan.Warnings = mountProbeFallbackWarnings(preparation)

	return preparation, nil
}

func prepareApplyEvidence(
	desired desiredApply,
	observation WorkloadObservation,
	transaction store.Transaction,
	hasTransaction bool,
	applied store.AppliedService,
	hasApplied bool,
) Preparation {
	return Preparation{
		Plan: Plan{
			Kind:        "",
			Project:     desired.project,
			Service:     desired.workload.ServiceName,
			Runtime:     desired.execution.Kind,
			Platform:    desired.execution.Platform,
			Image:       desired.image,
			Source:      desired.workload.SourceDigest,
			Desired:     desired.workload.EffectiveDigest,
			Observation: observation,
		},
		Workload:       desired.workload,
		Execution:      desired.execution,
		Transaction:    transaction,
		HasTransaction: hasTransaction,
		Applied:        applied,
		HasApplied:     hasApplied,
		Actions:        nil,
	}
}

func (service *Service) prepareRecovery(
	ctx context.Context,
	preparation Preparation,
) (Preparation, error) {
	transaction := preparation.Transaction
	if !transactionMatches(transaction, preparation.Workload, preparation.Execution) ||
		!transactionMatchesApplied(transaction, preparation.Applied, preparation.HasApplied) {
		return Preparation{}, ErrConflictingState
	}

	actions, err := service.transactions.Actions(ctx, transaction.ID)
	if err != nil {
		return Preparation{}, fmt.Errorf("prepare apply actions: %w", err)
	}

	kind, err := classifyRecovery(transaction, actions)
	if err != nil {
		return Preparation{}, err
	}

	preparation.Plan.Kind = kind
	preparation.Actions = actions

	return preparation, nil
}

func validRuntimeEvidence(evidence RuntimeEvidence) bool {
	return evidence.Kind.SupportsWorkloads() && evidence.Platform.OS != "" &&
		evidence.Platform.Architecture != ""
}

func validWorkloadObservation(observation WorkloadObservation, workload domain.DesiredWorkload) bool {
	switch observation.State {
	case WorkloadObservationUnknown:
		return false
	case WorkloadObservationMissing:
		return validMissingWorkloadObservation(observation)
	case WorkloadObservationPresent:
		return validPresentWorkloadObservation(observation) &&
			workloadStorageMatches(observation.StorageDigest, observation.RuntimeMounts, workload)
	default:
		return false
	}
}

func validMissingWorkloadObservation(observation WorkloadObservation) bool {
	return observation.ID == "" && observation.ConfigurationDigest == (domain.Digest{}) &&
		observation.StorageDigest == (domain.Digest{}) && observation.RuntimeMounts == nil &&
		!observation.ConfigurationMatches && !observation.Running &&
		observation.Ownership == (domain.WorkloadOwnership{})
}

func validPresentWorkloadObservation(observation WorkloadObservation) bool {
	return observation.ID != "" && observation.ConfigurationDigest != (domain.Digest{}) &&
		observation.StorageDigest != (domain.Digest{}) &&
		observation.Ownership.Status <= domain.OwnershipManaged
}

func transactionMatches(
	transaction store.Transaction,
	workload domain.DesiredWorkload,
	execution RuntimeEvidence,
) bool {
	return transaction.Runtime == execution.Kind && transaction.SourceDigest == workload.SourceDigest &&
		transaction.EffectiveDigest == workload.EffectiveDigest &&
		transaction.ExecutionDigest == execution.Digest
}

func transactionMatchesApplied(
	transaction store.Transaction,
	applied store.AppliedService,
	found bool,
) bool {
	switch transaction.Kind {
	case store.TransactionBootstrap, store.TransactionAdopt:
		return !found && !transaction.HasBaseTransaction
	case store.TransactionUpgrade:
		return found && transaction.HasBaseTransaction &&
			transaction.BaseTransactionID == applied.TransactionID &&
			transaction.PredecessorWorkloadID == applied.WorkloadID
	default:
		return false
	}
}
