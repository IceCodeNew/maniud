package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type discardRuntimeStub struct {
	probe    WorkloadEffectProbe
	probeErr error
	applyErr error
}

type mutationRuntimeFixture struct {
	*testRuntime
	*upgradeRuntimeFixture
}

func (runtime discardRuntimeStub) DiscardWorkload(context.Context, domain.DesiredWorkload, string) error {
	return runtime.applyErr
}

func (runtime discardRuntimeStub) ProbeDiscardedWorkload(
	context.Context,
	domain.DesiredWorkload,
	string,
) (WorkloadEffectProbe, error) {
	return runtime.probe, runtime.probeErr
}

func TestWorkloadDiscardEffectBoundaries(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	transaction := "transaction"
	effect := workloadDiscardEffect{workload: workload, transaction: transaction}

	effect.runtime = discardRuntimeStub{applyErr: errTestBoundary}
	if !errors.Is(effect.Apply(context.Background()), errTestBoundary) {
		t.Fatal("discard apply did not preserve runtime error")
	}

	cases := []struct {
		name          string
		probe         WorkloadEffectProbe
		err           error
		wantSatisfied bool
	}{
		{testProbeErrorName, WorkloadEffectProbe{}, errTestBoundary, false},
		{
			"missing evidence",
			WorkloadEffectProbe{
				State:    WorkloadEffectProbeMissing,
				Workload: createdWorkloadEffectEvidence(workload, transaction),
			},
			nil,
			false,
		},
		{
			testMissingValue,
			WorkloadEffectProbe{State: WorkloadEffectProbeMissing, Workload: emptyWorkloadEffectEvidence()},
			nil,
			true,
		},
		{
			"observed mismatch",
			WorkloadEffectProbe{State: WorkloadEffectProbeObserved, Workload: emptyWorkloadEffectEvidence()},
			nil,
			false,
		},
		{eventUnknown, WorkloadEffectProbe{State: WorkloadEffectProbeUnknown}, nil, false},
		{testInvalidValue, WorkloadEffectProbe{State: WorkloadEffectProbeState(99)}, nil, false},
	}
	for _, test := range cases {
		effect.runtime = discardRuntimeStub{probe: test.probe, probeErr: test.err}
		result, err := effect.Probe(context.Background())
		if test.wantSatisfied != result.Satisfied ||
			((test.err != nil || !test.wantSatisfied) != (err != nil)) {
			t.Fatalf("Probe(%s) = %#v, %v", test.name, result, err)
		}
	}
}

func TestDiscardWorkloadEvidenceValidation(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	transaction := "transaction"

	observed := createdWorkloadEffectEvidence(workload, transaction)
	for _, lifecycle := range []WorkloadLifecycle{
		WorkloadLifecycleCreated,
		WorkloadLifecycleRunning,
		WorkloadLifecycleExited,
	} {
		observed.Lifecycle = lifecycle
		if !discardWorkloadMatches(observed, workload, transaction) {
			t.Fatalf("valid lifecycle %q rejected", lifecycle)
		}
	}
	mutations := []func(*WorkloadEffectEvidence){
		func(e *WorkloadEffectEvidence) { e.ConfigurationMatches = false },
		func(e *WorkloadEffectEvidence) { e.Lifecycle = WorkloadLifecycleUnknown },
		func(e *WorkloadEffectEvidence) { e.ID = "" },
		func(e *WorkloadEffectEvidence) { e.Name = testOtherValue },
		func(e *WorkloadEffectEvidence) { e.ConfigurationDigest = domain.Digest{} },
		func(e *WorkloadEffectEvidence) { e.StorageDigest = domain.Hash([]byte(testOtherValue)) },
		func(e *WorkloadEffectEvidence) { e.RuntimeMounts = []domain.RuntimeMount{} },
		func(e *WorkloadEffectEvidence) { e.Ownership.ImageConfig = domain.Hash([]byte(testOtherValue)) },
		func(e *WorkloadEffectEvidence) {
			e.Ownership.PlatformManifest = domain.Hash([]byte(testOtherValue))
		},
	}
	for index, mutate := range mutations {
		candidate := observed
		mutate(&candidate)
		if discardWorkloadMatches(candidate, workload, transaction) {
			t.Fatalf("invalid evidence mutation %d accepted", index)
		}
	}
}

func TestRunWorkloadDiscardRejectsInvalidJournal(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{}
	_, err := runWorkloadDiscard(
		context.Background(),
		nil,
		identifier,
		1,
		workload,
		discardRuntimeStub{},
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("zero transaction accepted: %v", err)
	}
}

func TestCompletedUpgradeImageEvidence(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	preparation := mutation.preparation
	intent := imagePullIntent(1, preparation.Workload.Image)
	action := store.Action{
		TransactionID: preparation.Transaction.ID,
		Sequence:      1,
		Kind:          intent.Kind,
		State:         store.ActionStateCompleted,
		IntentDigest:  intent.IntentDigest,
	}

	_, err := completedUpgradeImagePull(context.Background(), action, runtime, preparation)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatal("completed image pull accepted missing digest")
	}
	missing := imageEffectDigest(imageEffectMissing, preparation.Workload.Image)
	action.PostconditionDigest = &missing
	result, err := completedUpgradeImagePull(context.Background(), action, runtime, preparation)
	if err != nil || result.Satisfied {
		t.Fatalf("missing image result = %#v, %v", result, err)
	}
	bad := domain.Hash([]byte("bad"))
	action.PostconditionDigest = &bad
	_, err = completedUpgradeImagePull(context.Background(), action, runtime, preparation)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatal("bad image digest accepted")
	}
	observed := imageEffectDigest(imageEffectObserved, preparation.Workload.Image)
	action.PostconditionDigest = &observed
	runtime.imageProbeErrAt[1] = errTestBoundary
	_, err = completedUpgradeImagePull(context.Background(), action, runtime, preparation)
	if !errors.Is(err, errTestBoundary) {
		t.Fatal("image probe error lost")
	}
	delete(runtime.imageProbeErrAt, 1)
	runtime.image = ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()}
	_, err = completedUpgradeImagePull(context.Background(), action, runtime, preparation)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatal("completed image without runtime proof accepted")
	}
	runtime.image = observedImageProbe(preparation.Workload.Image)
	result, err = completedUpgradeImagePull(context.Background(), action, runtime, preparation)
	if err != nil || !result.Satisfied {
		t.Fatalf("completed image rejected: %#v %v", result, err)
	}
}

func TestSettleUpgradeImagePullRequiresMatchingIntent(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	preparation := mutation.preparation
	intent := imagePullIntent(1, preparation.Workload.Image)
	observed := imageEffectDigest(imageEffectObserved, preparation.Workload.Image)
	bad := domain.Hash([]byte("bad"))
	action := completedEffectAction(preparation.Transaction.ID, intent, observed)
	runtime.image = observedImageProbe(preparation.Workload.Image)
	action.IntentDigest = bad
	_, err := settleUpgradeImagePull(
		context.Background(),
		mutation,
		runtime,
		bootstrapCredentials{},
		action,
	)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatal("mismatched pull intent accepted")
	}
	action.IntentDigest = intent.IntentDigest
	result, err := settleUpgradeImagePull(
		context.Background(),
		mutation,
		runtime,
		bootstrapCredentials{},
		action,
	)
	if err != nil || !result.Satisfied {
		t.Fatalf("settled pull = %#v, %v", result, err)
	}
}

func TestCompletedUpgradeTransitionEvidence(t *testing.T) {
	t.Parallel()

	mutation := newUpgradeMutationWithCleanup(t)
	journey := newUpgradeJourney(mutation.preparation)
	transition := journey.stop
	completed := store.Action{PostconditionDigest: nil}
	if _, err := completedTransitionResult(completed, transition); !errors.Is(err, ErrConflictingState) {
		t.Fatal("transition accepted nil digest")
	}
	satisfied := workloadTransitionPostcondition(transition, true).Digest
	completed.PostconditionDigest = &satisfied
	if ok, err := completedTransitionResult(completed, transition); err != nil || !ok {
		t.Fatal("satisfied transition rejected")
	}
	negative := workloadTransitionPostcondition(transition, false).Digest
	completed.PostconditionDigest = &negative
	if ok, err := completedTransitionResult(completed, transition); err != nil || ok {
		t.Fatal("negative transition rejected")
	}
	bad := domain.Hash([]byte("bad"))
	completed.PostconditionDigest = &bad
	if _, err := completedTransitionResult(completed, transition); !errors.Is(err, ErrConflictingState) {
		t.Fatal("bad transition digest accepted")
	}
	if !resolvedNegative(EffectPostcondition{Digest: negative}) || resolvedNegative(EffectPostcondition{}) {
		t.Fatal("resolved-negative classification incorrect")
	}
}

func TestUpgradeOwnershipAndActionValidation(t *testing.T) {
	t.Parallel()

	mutation := newUpgradeMutationWithCleanup(t)
	adopted := mutation.preparation.Applied
	adopted.Kind = store.TransactionAdopt
	if appliedWorkloadOwnership(adopted, testServiceName).Status != domain.OwnershipUnmanaged {
		t.Fatal("adopt predecessor was treated as managed")
	}
	invalidOrder := validUpgradeActions([]store.Action{{Kind: workloadRenameActionKind}})
	if invalidOrder || validUpgradeActions(make([]store.Action, 10)) {
		t.Fatal("invalid upgrade action sequence accepted")
	}
}

func newUpgradeMutationWithCleanup(
	t *testing.T,
) *boundMutation {
	t.Helper()

	state, mutation, _ := newUpgradeMutation(t)
	t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })

	return mutation
}

func TestRestorePendingActionValidation(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{workloadDiscardActionKind, workloadRestoreStartActionKind} {
		if !validRestorePendingAction(nil, store.Action{Kind: kind}) {
			t.Fatalf("restore pending %q rejected", kind)
		}
	}
	if validRestorePendingAction(nil, store.Action{Kind: workloadRenameActionKind}) ||
		validRestorePendingAction(nil, store.Action{Kind: testOtherValue}) {
		t.Fatal("unsafe restore pending action accepted")
	}
	completedDiscard := []store.Action{{
		Kind:  workloadDiscardActionKind,
		State: store.ActionStateCompleted,
	}}
	if !validRestorePendingAction(completedDiscard, store.Action{Kind: workloadRenameActionKind}) {
		t.Fatal("rename after discard rejected")
	}
}

//nolint:cyclop // The test keeps the restore helper boundary matrix in one audit surface.
func TestRestoreJourneyPureHelpers(t *testing.T) {
	t.Parallel()

	state, mutation, _ := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	mutation.preparation.Plan.Kind = PlanRestore
	mutation.preparation.Transaction.State = store.TransactionDegraded
	if validRestoreMutation(nil) {
		t.Fatal("nil restore mutation validated")
	}
	if _, err := prepareRestoreJourney(context.Background(), nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("nil restore mutation accepted")
	}
	if _, err := prepareRestoreJourney(context.Background(), mutation); !errors.Is(err, ErrConflictingState) {
		t.Fatal("actionless degraded upgrade accepted")
	}

	journey := newUpgradeJourney(mutation.preparation)
	intents := upgradeCoreIntents(mutation.preparation, journey, backup.Publication{})
	if len(intents) != 5 {
		t.Fatalf("core intents = %d", len(intents))
	}
	withPull := mutation.preparation
	withPull.Actions = []store.Action{{Kind: imagePullActionKind}, {Kind: workloadStopActionKind}}
	if len(upgradeCoreIntents(withPull, journey, backup.Publication{})) != 6 || coreStopIndex(withPull.Actions) != 1 {
		t.Fatal("image pull core offset incorrect")
	}
	if coreStopIndex(nil) != -1 ||
		restoreCoreActionCount([]store.Action{{Kind: testInvalidValue}}, intents) != 0 {
		t.Fatal("core action scan incorrect")
	}
	if coreContainsAction(nil, "x") || !coreContainsAction([]store.Action{{Kind: "x"}}, "x") {
		t.Fatal("core action lookup incorrect")
	}
}

func TestRunBoundMutationDispatchContract(t *testing.T) {
	t.Parallel()
	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	boundRuntime := &mutationRuntimeFixture{
		testRuntime: &testRuntime{
			inspect: func(context.Context) (RuntimeEvidence, error) { return testExecutionEvidence(), nil },
			check:   func(domain.DesiredWorkload) error { return nil },
			observe: func(context.Context, domain.DesiredWorkload) (WorkloadObservation, error) {
				return missingObservation(), nil
			},
		},
		upgradeRuntimeFixture: runtime,
	}

	unchanged := &boundMutation{preparation: Preparation{Plan: Plan{Kind: PlanUnchanged}}}
	if err := runBoundMutation(context.Background(), unchanged, boundRuntime, bootstrapCredentials{}); err != nil {
		t.Fatalf("unchanged dispatch = %v", err)
	}
	unchanged.preparation.Plan.Kind = PlanBootstrap
	err := runBoundMutation(context.Background(), unchanged, boundRuntime, bootstrapCredentials{})
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("transactionless mutation = %v", err)
	}

	invalid := *mutation
	invalid.preparation.Transaction.Kind = store.TransactionKind(testInvalidValue)
	err = runBoundMutation(context.Background(), &invalid, boundRuntime, bootstrapCredentials{})
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("unknown transaction dispatch = %v", err)
	}
	invalid.preparation.Transaction.Kind = store.TransactionAdopt
	err = runBoundMutation(context.Background(), &invalid, boundRuntime, bootstrapCredentials{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("adopt dispatch = %v", err)
	}
	invalid.preparation.Transaction.Kind = store.TransactionUpgrade
	invalid.preparation.Transaction.State = store.TransactionDegraded
	err = runBoundMutation(context.Background(), &invalid, boundRuntime, bootstrapCredentials{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("restore dispatch = %v", err)
	}

	if err := runBoundMutation(context.Background(), mutation, boundRuntime, bootstrapCredentials{}); err != nil {
		t.Fatalf("upgrade dispatch = %v", err)
	}
}

func TestServiceApplyBootstrapContract(t *testing.T) {
	t.Parallel()
	operation := newTestOperation(t)
	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)
	workload := testWorkloadEffect(t)
	runtime := &mutationRuntimeFixture{
		testRuntime:           operation.runtime,
		upgradeRuntimeFixture: newUpgradeRuntime(workload, upgradeJourney{}),
	}
	runtime.image = ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()}
	service := NewService(operation.service.images, runtime, state)

	plan, err := service.Apply(context.Background(), operation.request, state, bootstrapCredentials{})
	if err != nil || plan.Kind != PlanBootstrap {
		t.Fatalf("Apply() = %#v, %v", plan, err)
	}
	if _, err = service.Apply(context.Background(), operation.request, state, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Apply(nil auth) = %v", err)
	}
	var missing *Service
	if _, err = missing.Apply(
		context.Background(), operation.request, state, bootstrapCredentials{},
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Apply(nil service) = %v", err)
	}
	_, err = service.Apply(context.Background(), operation.request, nil, bootstrapCredentials{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Apply(nil state) = %v", err)
	}
	plain := NewService(operation.service.images, operation.runtime, state)
	_, err = plain.Apply(context.Background(), operation.request, state, bootstrapCredentials{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Apply(read-only runtime) = %v", err)
	}
}

func TestServiceApplyRejectsUnpublishableRepositoryRuntime(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)
	workload := testWorkloadEffect(t)
	runtime := &mutationRuntimeFixture{
		testRuntime:           operation.runtime,
		upgradeRuntimeFixture: newUpgradeRuntime(workload, upgradeJourney{}),
	}
	runtime.image = ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()}
	service := NewService(operation.service.images, runtime, state)

	runtimeBase := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(runtimeBase, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation.request.Source = repositoryRuntimeSource(t, runtimeBase)

	plan, err := service.Apply(context.Background(), operation.request, state, bootstrapCredentials{})
	if !errors.Is(err, compose.ErrInvalidSource) || plan.Kind != "" {
		t.Fatalf("Apply(unpublishable runtime) = %#v, %v", plan, err)
	}
}

func repositoryRuntimeSource(t *testing.T, runtimeBase string) compose.Source {
	t.Helper()

	content := []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    volumes:
      - ./data:/data:ro
`)
	files := map[string]compose.RepositoryFile{
		"compose.yaml": {Content: content},
		"data/value":   {Content: []byte("committed\n")},
	}
	source, err := compose.CaptureRepositorySource(
		t.TempDir(),
		"compose.yaml",
		nil,
		func(name string) (compose.RepositoryFile, bool, error) {
			file, found := files[name]

			return file, found, nil
		},
		func(name string) (compose.RepositoryPathSnapshot, error) {
			if name != testDataName {
				return compose.RepositoryPathSnapshot{}, compose.ErrInvalidSource
			}

			return compose.RepositoryPathSnapshot{
				Directory: true,
				Files:     map[string]compose.RepositoryFile{"data/value": files["data/value"]},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	source, err = compose.PinRepositoryRuntime(source, runtimeBase)
	if err != nil {
		t.Fatalf("PinRepositoryRuntime() error = %v", err)
	}

	return source
}

func TestMutationRepositoryRuntimeSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		preparation Preparation
		want        bool
	}{
		{name: "no transaction", preparation: Preparation{}, want: false},
		{
			name: "bootstrap transaction",
			preparation: Preparation{
				HasTransaction: true,
				Transaction:    store.Transaction{Kind: store.TransactionBootstrap},
			},
			want: true,
		},
		{
			name: "active upgrade",
			preparation: Preparation{
				HasTransaction: true,
				Transaction: store.Transaction{
					Kind: store.TransactionUpgrade, State: store.TransactionActive,
				},
			},
			want: true,
		},
		{
			name: "degraded upgrade",
			preparation: Preparation{
				HasTransaction: true,
				Transaction: store.Transaction{
					Kind: store.TransactionUpgrade, State: store.TransactionDegraded,
				},
			},
			want: false,
		},
		{
			name: "adopt transaction",
			preparation: Preparation{
				HasTransaction: true,
				Transaction:    store.Transaction{Kind: store.TransactionAdopt},
			},
			want: false,
		},
	}
	for _, test := range tests {
		if got := mutationNeedsRepositoryRuntime(test.preparation); got != test.want {
			t.Errorf("mutationNeedsRepositoryRuntime(%s) = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestMaterializeMutationRuntimeContainsFailures(t *testing.T) {
	t.Parallel()

	if err := materializeMutationRuntime(nil); err != nil {
		t.Fatalf("materializeMutationRuntime(nil) error = %v", err)
	}
	mutation := &boundMutation{materialize: func() error { return errTestBoundary }}
	if err := materializeMutationRuntime(mutation); !errors.Is(err, errTestBoundary) {
		t.Fatalf("materializeMutationRuntime(failure) error = %v", err)
	}
}

func TestServiceApplyReturnsMutationFailure(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)
	workload := testWorkloadEffect(t)
	runtime := &mutationRuntimeFixture{
		testRuntime:           operation.runtime,
		upgradeRuntimeFixture: newUpgradeRuntime(workload, upgradeJourney{}),
	}
	runtime.image = ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()}
	runtime.pullUnchanged = true
	service := NewService(operation.service.images, runtime, state)

	plan, err := service.Apply(context.Background(), operation.request, state, bootstrapCredentials{})
	if !errors.Is(err, ErrConflictingState) || plan.Kind != "" {
		t.Fatalf("Apply(negative image pull) = %#v, %v", plan, err)
	}
}
