package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	testProjectName = "example"
	testServiceName = "api"
	eventJournal    = "journal"
)

var errTestBoundary = errors.New("test boundary failed")

type imageResolverFunc func(context.Context, imageref.Source, domain.Platform) (domain.ImageIdentity, error)

func (function imageResolverFunc) Resolve(
	ctx context.Context,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	return function(ctx, source, platform)
}

type testRuntime struct {
	inspect func(context.Context) (RuntimeEvidence, error)
	check   func(domain.DesiredWorkload) error
	observe func(context.Context, domain.DesiredWorkload) (WorkloadObservation, error)
}

func (runtime testRuntime) Inspect(ctx context.Context) (RuntimeEvidence, error) {
	return runtime.inspect(ctx)
}

func (runtime testRuntime) CheckWorkload(workload domain.DesiredWorkload) error {
	return runtime.check(workload)
}

func (runtime testRuntime) ObserveWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
) (WorkloadObservation, error) {
	return runtime.observe(ctx, workload)
}

type testTransactions struct {
	unresolved func(context.Context, string, string) (store.Transaction, bool, error)
	actions    func(context.Context, store.TransactionID) ([]store.Action, error)
}

func (transactions testTransactions) UnresolvedTransaction(
	ctx context.Context,
	project string,
	service string,
) (store.Transaction, bool, error) {
	return transactions.unresolved(ctx, project, service)
}

func (transactions testTransactions) Actions(
	ctx context.Context,
	identifier store.TransactionID,
) ([]store.Action, error) {
	return transactions.actions(ctx, identifier)
}

type testOperation struct {
	service      *Service
	request      Request
	runtime      *testRuntime
	transactions *testTransactions
	events       *[]string
}

func newTestOperation(t *testing.T) testOperation {
	t.Helper()

	events := make([]string, 0, 5)
	execution := testExecutionEvidence()
	projected := new(domain.DesiredWorkload)
	resolver := newTestImageResolver(&events, execution)
	runtime := newTestRuntime(&events, execution, projected)
	transactions := newTestTransactions(&events)
	request := newTestRequest(t)

	return testOperation{
		service:      NewService(resolver, runtime, transactions),
		request:      request,
		runtime:      runtime,
		transactions: transactions,
		events:       &events,
	}
}

func testExecutionEvidence() RuntimeEvidence {
	return RuntimeEvidence{
		Kind: domain.RuntimeDocker,
		Platform: domain.Platform{
			OS:           "linux",
			Architecture: "amd64",
			Variant:      "",
		},
		Digest: domain.Hash([]byte("execution")),
	}
}

func newTestImageResolver(events *[]string, execution RuntimeEvidence) imageResolverFunc {
	referenceDigest := domain.Hash([]byte("reference"))
	platformManifest := domain.Hash([]byte("platform manifest"))
	imageConfig := domain.Hash([]byte("image config"))

	return imageResolverFunc(func(
		_ context.Context,
		source imageref.Source,
		platform domain.Platform,
	) (domain.ImageIdentity, error) {
		*events = append(*events, "resolve")

		if source.String() != "example.com/team/api:1" || platform != execution.Platform {
			return emptyImageIdentity(), errTestBoundary
		}

		reference, err := source.Pin(referenceDigest)
		if err != nil {
			return emptyImageIdentity(), fmt.Errorf("pin test reference: %w", err)
		}

		return domain.ImageIdentity{
			Reference:        reference.String(),
			ReferenceDigest:  referenceDigest,
			Platform:         platform,
			PlatformManifest: platformManifest,
			ImageConfig:      imageConfig,
		}, nil
	})
}

func newTestRuntime(
	events *[]string,
	execution RuntimeEvidence,
	projected *domain.DesiredWorkload,
) *testRuntime {
	return &testRuntime{
		inspect: func(context.Context) (RuntimeEvidence, error) {
			*events = append(*events, "inspect")

			return execution, nil
		},
		check: func(workload domain.DesiredWorkload) error {
			*events = append(*events, "check")
			*projected = workload

			return nil
		},
		observe: func(_ context.Context, workload domain.DesiredWorkload) (WorkloadObservation, error) {
			*events = append(*events, "observe")

			if workload.EffectiveDigest != projected.EffectiveDigest {
				return emptyObservation(), errTestBoundary
			}

			return missingObservation(), nil
		},
	}
}

func newTestTransactions(events *[]string) *testTransactions {
	return &testTransactions{
		unresolved: func(_ context.Context, project, service string) (store.Transaction, bool, error) {
			*events = append(*events, eventJournal)

			if project != testProjectName || service != testServiceName {
				return emptyTransaction(), false, errTestBoundary
			}

			return emptyTransaction(), false, nil
		},
		actions: func(context.Context, store.TransactionID) ([]store.Action, error) {
			*events = append(*events, "actions")

			return nil, nil
		},
	}
}

func newTestRequest(t *testing.T) Request {
	t.Helper()

	return Request{
		Source: compose.Source{
			Content: []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`),
			WorkingDir:  filepath.Clean(t.TempDir()),
			Environment: nil,
			Profiles:    nil,
		},
		Service: testServiceName,
	}
}

func emptyImageIdentity() domain.ImageIdentity {
	return domain.ImageIdentity{
		Reference:        "",
		ReferenceDigest:  domain.Digest{},
		Platform:         domain.Platform{OS: "", Architecture: "", Variant: ""},
		PlatformManifest: domain.Digest{},
		ImageConfig:      domain.Digest{},
	}
}

func emptyObservation() WorkloadObservation {
	return WorkloadObservation{
		State:                WorkloadObservationUnknown,
		ConfigurationMatches: false,
		Running:              false,
		Ownership:            testOwnership(domain.OwnershipConflicting),
	}
}

func emptyDesiredWorkload() domain.DesiredWorkload {
	return domain.DesiredWorkload{
		ServiceName:     "",
		ContainerName:   "",
		Image:           emptyImageIdentity(),
		Entrypoint:      nil,
		Command:         nil,
		SourceDigest:    domain.Digest{},
		EffectiveDigest: domain.Digest{},
	}
}

func missingObservation() WorkloadObservation {
	observation := emptyObservation()
	observation.State = WorkloadObservationMissing

	return observation
}

func testOwnership(status domain.OwnershipStatus) domain.WorkloadOwnership {
	return domain.WorkloadOwnership{
		Status:           status,
		Service:          "",
		Transaction:      "",
		DesiredState:     domain.Digest{},
		Reference:        domain.Digest{},
		ImageConfig:      domain.Digest{},
		PlatformManifest: domain.Digest{},
	}
}

func emptyTransaction() store.Transaction {
	return store.Transaction{
		ID:              store.TransactionID{},
		State:           "",
		Runtime:         "",
		SourceDigest:    domain.Digest{},
		EffectiveDigest: domain.Digest{},
		ExecutionDigest: domain.Digest{},
	}
}

func exactTransaction(preparation Preparation, state store.TransactionState) store.Transaction {
	return store.Transaction{
		ID:              store.TransactionID{1},
		State:           state,
		Runtime:         preparation.Execution.Kind,
		SourceDigest:    preparation.Workload.SourceDigest,
		EffectiveDigest: preparation.Workload.EffectiveDigest,
		ExecutionDigest: preparation.Execution.Digest,
	}
}

func action(
	transaction store.Transaction,
	sequence int64,
	state store.ActionState,
) store.Action {
	postcondition := (*domain.Digest)(nil)

	if state == store.ActionStateCompleted {
		value := domain.Hash([]byte("postcondition"))
		postcondition = &value
	}

	return store.Action{
		TransactionID:       transaction.ID,
		Sequence:            sequence,
		Kind:                "workload.create",
		State:               state,
		IntentDigest:        domain.Hash([]byte("intent")),
		PostconditionDigest: postcondition,
	}
}
