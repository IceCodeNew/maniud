package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/store"
)

type operationMutation struct {
	name   string
	mutate func(*testOperation)
}

func TestPrepareContainsDependencyFailures(t *testing.T) {
	t.Parallel()

	tests := append(desiredFailureTests(), evidenceFailureTests()...)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operation := newTestOperation(t)
			test.mutate(&operation)

			_, err := operation.service.Prepare(context.Background(), operation.request)
			if err == nil {
				t.Fatal("Prepare() succeeded")
			}
		})
	}
}

func desiredFailureTests() []operationMutation {
	return []operationMutation{
		{
			name: "invalid source",
			mutate: func(operation *testOperation) {
				operation.request.Source.Content = []byte("invalid: [")
			},
		},
		{
			name: "missing service",
			mutate: func(operation *testOperation) {
				operation.request.Service = "worker"
			},
		},
		{
			name: "runtime inspect",
			mutate: func(operation *testOperation) {
				operation.runtime.inspect = func(context.Context) (RuntimeEvidence, error) {
					return RuntimeEvidence{}, errTestBoundary
				}
			},
		},
		{
			name: "invalid runtime evidence",
			mutate: func(operation *testOperation) {
				operation.runtime.inspect = func(context.Context) (RuntimeEvidence, error) {
					return RuntimeEvidence{
						Kind:     domain.RuntimeContainerd,
						Platform: domain.Platform{OS: "", Architecture: "", Variant: ""},
						Digest:   domain.Digest{},
					}, nil
				}
			},
		},
		{
			name: "image",
			mutate: func(operation *testOperation) {
				operation.service.images = failingImageResolver(errTestBoundary)
			},
		},
		{
			name: "image projection",
			mutate: func(operation *testOperation) {
				operation.service.images = failingImageResolver(nil)
			},
		},
		{
			name: "capability",
			mutate: func(operation *testOperation) {
				operation.runtime.check = func(domain.DesiredWorkload) error { return errTestBoundary }
			},
		},
	}
}

func TestPrepareRejectsRuntimeProvenanceMismatch(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	original := operation.runtime.inspect
	operation.runtime.inspect = func(ctx context.Context) (RuntimeEvidence, error) {
		evidence, err := original(ctx)
		evidence.Kind = domain.RuntimePodman

		return evidence, err
	}

	_, err := operation.service.Prepare(context.Background(), operation.request)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Prepare(runtime provenance mismatch) error = %v", err)
	}
}

func evidenceFailureTests() []operationMutation {
	return []operationMutation{
		{
			name: eventJournal,
			mutate: func(operation *testOperation) {
				operation.transactions.unresolved = func(
					context.Context,
					string,
					string,
				) (store.Transaction, bool, error) {
					return emptyTransaction(), false, errTestBoundary
				}
			},
		},
		{
			name: eventApplied,
			mutate: func(operation *testOperation) {
				operation.transactions.applied = func(
					context.Context,
					string,
					string,
				) (store.AppliedService, bool, error) {
					return store.AppliedService{}, false, errTestBoundary
				}
			},
		},
		{
			name: "observation",
			mutate: func(operation *testOperation) {
				operation.runtime.observe = func(
					context.Context,
					domain.DesiredWorkload,
				) (WorkloadObservation, error) {
					return emptyObservation(), errTestBoundary
				}
			},
		},
		{
			name: "invalid observation",
			mutate: func(operation *testOperation) {
				operation.runtime.observe = func(
					context.Context,
					domain.DesiredWorkload,
				) (WorkloadObservation, error) {
					invalid := missingObservation()
					invalid.Running = true

					return invalid, nil
				}
			},
		},
	}
}

func failingImageResolver(resolveErr error) imageResolverFunc {
	return func(
		context.Context,
		imageref.Source,
		domain.Platform,
	) (domain.ImageIdentity, error) {
		return emptyImageIdentity(), resolveErr
	}
}

func TestPrepareRejectsInvalidServiceAndContext(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)

	invalidServices := []*Service{
		nil,
		{},
		NewService(nil, operation.service.runtime, operation.service.transactions),
		NewService(operation.service.images, nil, operation.service.transactions),
		NewService(operation.service.images, operation.service.runtime, nil),
	}
	for index, service := range invalidServices {
		_, err := service.Prepare(context.Background(), operation.request)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Prepare(invalid service %d) error = %v", index, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := operation.service.Prepare(ctx, operation.request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare(cancelled) error = %v", err)
	}

	_, err = invalidServices[0].DryRun(context.Background(), operation.request)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("DryRun(invalid service) error = %v", err)
	}
}
