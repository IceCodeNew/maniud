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
}

// NewService creates an apply service over explicit read-only dependencies.
func NewService(images ImageResolver, runtime Runtime, transactions TransactionReader) *Service {
	return &Service{images: images, runtime: runtime, transactions: transactions}
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

	return preparation.Plan, nil
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

	imageSource, err := project.ImageSource(request.Service)
	if err != nil {
		return empty, fmt.Errorf("prepare apply service: %w", err)
	}

	execution, err := service.runtime.Inspect(ctx)
	if err != nil {
		return empty, fmt.Errorf("prepare apply runtime: %w", err)
	}

	if !validRuntimeEvidence(execution) {
		return empty, ErrInvalidRequest
	}

	image, err := service.images.Resolve(ctx, imageSource, execution.Platform)
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

	observation, err := service.runtime.ObserveWorkload(ctx, desired.workload)
	if err != nil {
		return empty, fmt.Errorf("prepare apply observation: %w", err)
	}

	if !validWorkloadObservation(observation) {
		return empty, ErrConflictingState
	}

	preparation := Preparation{
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
		HasTransaction: found,
		Actions:        nil,
	}

	if found {
		return service.prepareRecovery(ctx, preparation)
	}

	preparation.Plan.Kind, err = classifyNewApply(observation, desired.workload)
	if err != nil {
		return empty, err
	}

	return preparation, nil
}

func (service *Service) prepareRecovery(
	ctx context.Context,
	preparation Preparation,
) (Preparation, error) {
	transaction := preparation.Transaction
	if !transactionMatches(transaction, preparation.Workload, preparation.Execution) {
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

func validWorkloadObservation(observation WorkloadObservation) bool {
	emptyOwnership := domain.WorkloadOwnership{
		Status:           domain.OwnershipConflicting,
		Service:          "",
		Transaction:      "",
		DesiredState:     domain.Digest{},
		Reference:        domain.Digest{},
		ImageConfig:      domain.Digest{},
		PlatformManifest: domain.Digest{},
	}

	switch observation.State {
	case WorkloadObservationUnknown:
		return false
	case WorkloadObservationMissing:
		return !observation.ConfigurationMatches && !observation.Running &&
			observation.Ownership == emptyOwnership
	case WorkloadObservationPresent:
		return observation.Ownership.Status <= domain.OwnershipManaged
	default:
		return false
	}
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
