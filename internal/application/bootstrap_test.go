//nolint:goconst // Scenario labels remain beside the bootstrap cases they identify.
package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
	"github.com/IceCodeNew/maniud/internal/store"
)

type bootstrapRuntimeFixture struct {
	image           ImageProbe
	workload        WorkloadEffectProbe
	imageProbeCalls int
	imageProbeErrAt map[int]error
	createProbeErr  error
	startProbeErr   error
	pulls           int
	creates         int
	starts          int
}

func (runtime *bootstrapRuntimeFixture) PullImage(
	_ context.Context,
	expected domain.ImageIdentity,
	_ credential.Provider,
) error {
	runtime.pulls++
	runtime.image = observedImageProbe(expected)

	return nil
}

func (runtime *bootstrapRuntimeFixture) ProbeImage(
	_ context.Context,
	expected domain.ImageIdentity,
) (ImageProbe, error) {
	runtime.imageProbeCalls++
	if err := runtime.imageProbeErrAt[runtime.imageProbeCalls]; err != nil {
		return ImageProbe{State: ImageProbeUnknown, Image: emptyImageEvidence()}, err
	}

	if runtime.image.State == ImageProbeObserved && !runtime.image.Matches(expected) {
		return runtime.image, nil
	}

	return runtime.image, nil
}

func (runtime *bootstrapRuntimeFixture) CreateWorkload(
	_ context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	_ WorkloadCreateOptions,
) (string, error) {
	runtime.creates++
	runtime.workload = WorkloadEffectProbe{
		State:    WorkloadEffectProbeObserved,
		Workload: createdWorkloadEffectEvidence(workload, transaction),
	}

	return testWorkloadEffectID, nil
}

func (runtime *bootstrapRuntimeFixture) ProbeCreatedWorkload(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
	_ string,
) (WorkloadEffectProbe, error) {
	return runtime.workload, runtime.createProbeErr
}

func (runtime *bootstrapRuntimeFixture) StartWorkload(
	_ context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) error {
	runtime.starts++
	runtime.workload = WorkloadEffectProbe{
		State:    WorkloadEffectProbeObserved,
		Workload: startedWorkloadEffectEvidence(workload, transaction),
	}

	return nil
}

func (runtime *bootstrapRuntimeFixture) ProbeStartedWorkload(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
) (WorkloadEffectProbe, error) {
	return runtime.workload, runtime.startProbeErr
}

type bootstrapCredentials struct{}

func (bootstrapCredentials) Credentials(
	context.Context,
	imageref.Reference,
) (credential.Value, error) {
	return credential.Value{}, nil
}

func TestRunBootstrapPullsCreatesStartsAndCompletesTransaction(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()

	err := runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil {
		t.Fatalf("runBootstrap() error = %v", err)
	}

	if runtime.pulls != 1 || runtime.creates != 1 || runtime.starts != 1 ||
		mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("bootstrap effects = pull %d, create %d, start %d, transaction %#v",
			runtime.pulls,
			runtime.creates,
			runtime.starts,
			mutation.preparation.Transaction,
		)
	}

	assertBootstrapActions(t, state, mutation.preparation.Transaction.ID, []string{
		imagePullActionKind,
		workloadCreateActionKind,
		workloadStartActionKind,
	})
	assertCompletedBootstrap(t, state, mutation)
}

func TestRunBootstrapSkipsPullForProvenLocalImage(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()
	runtime.image = observedImageProbe(mutation.preparation.Workload.Image)

	err := runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || runtime.pulls != 0 || runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("runBootstrap(local image) = %v, pull %d, create %d, start %d",
			err,
			runtime.pulls,
			runtime.creates,
			runtime.starts,
		)
	}

	assertBootstrapActions(t, state, mutation.preparation.Transaction.ID, []string{
		workloadCreateActionKind,
		workloadStartActionKind,
	})
}

func TestRunBootstrapRevalidatesRuntimeSourceBeforeCreate(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime := missingBootstrapRuntime()
	runtime.image = observedImageProbe(mutation.preparation.Workload.Image)
	mutation.materialize = func() error { return errTestBoundary }

	err := runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || runtime.creates != 0 || runtime.starts != 0 {
		t.Fatalf("runBootstrap(materialize drift) = %v, creates %d, starts %d",
			err, runtime.creates, runtime.starts)
	}
}

func TestRunBootstrapRecoversUnknownPullWithoutReplay(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()
	runtime.imageProbeErrAt = map[int]error{2: errTestBoundary}

	err := runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || runtime.pulls != 1 {
		t.Fatalf("runBootstrap(interrupted pull) = %v, pulls %d", err, runtime.pulls)
	}

	actions, actionsErr := state.Actions(context.Background(), mutation.preparation.Transaction.ID)
	if actionsErr != nil || len(actions) != 1 || actions[0].State != store.ActionStateEffectOutcomeUnknown {
		t.Fatalf("interrupted actions = %#v, %v", actions, actionsErr)
	}

	mutation.preparation.Actions = actions
	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect

	err = runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || runtime.pulls != 1 || runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("runBootstrap(recovery) = %v, pull %d, create %d, start %d",
			err,
			runtime.pulls,
			runtime.creates,
			runtime.starts,
		)
	}
}

func TestRunBootstrapVerifiesCompletedPullBeforeResumingCreate(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()
	runtime.createProbeErr = errTestBoundary

	err := runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || runtime.pulls != 1 || runtime.creates != 1 {
		t.Fatalf("runBootstrap(interrupted create) = %v, pulls %d, creates %d", err, runtime.pulls, runtime.creates)
	}

	mutation.preparation.Actions, err = state.Actions(context.Background(), mutation.preparation.Transaction.ID)
	if err != nil {
		t.Fatalf("Actions() error = %v", err)
	}
	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect
	runtime.createProbeErr = nil

	err = runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || runtime.pulls != 1 || runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("runBootstrap(create recovery) = %v, pull %d, create %d, start %d",
			err,
			runtime.pulls,
			runtime.creates,
			runtime.starts,
		)
	}
}

func TestRunBootstrapRecoversUnknownStartWithoutReplay(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()
	runtime.image = observedImageProbe(mutation.preparation.Workload.Image)
	runtime.startProbeErr = errTestBoundary

	err := runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("runBootstrap(interrupted start) = %v, creates %d, starts %d", err, runtime.creates, runtime.starts)
	}

	mutation.preparation.Actions, err = state.Actions(context.Background(), mutation.preparation.Transaction.ID)
	if err != nil {
		t.Fatalf("Actions() error = %v", err)
	}
	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect
	runtime.startProbeErr = nil

	err = runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("runBootstrap(start recovery) = %v, creates %d, starts %d", err, runtime.creates, runtime.starts)
	}
}

func TestRunBootstrapResumesCompletedCreateAfterStart(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()
	runtime.image = observedImageProbe(mutation.preparation.Workload.Image)

	create, err := runWorkloadCreate(
		context.Background(),
		mutation.lock,
		mutation.preparation.Transaction.ID,
		1,
		mutation.preparation.Workload,
		runtime,
		defaultWorkloadCreateOptions(),
	)
	if err != nil || !create.Satisfied {
		t.Fatalf("runWorkloadCreate() = %#v, %v", create, err)
	}

	start, err := runWorkloadStart(
		context.Background(),
		mutation.lock,
		mutation.preparation.Transaction.ID,
		2,
		mutation.preparation.Workload,
		runtime,
	)
	if err != nil || !start.Satisfied {
		t.Fatalf("runWorkloadStart() = %#v, %v", start, err)
	}

	mutation.preparation.Actions, err = state.Actions(
		context.Background(),
		mutation.preparation.Transaction.ID,
	)
	if err != nil {
		t.Fatalf("Actions() error = %v", err)
	}
	mutation.preparation.Plan.Kind = PlanResume

	err = runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("runBootstrap(completed recovery) = %v, create %d, start %d",
			err,
			runtime.creates,
			runtime.starts,
		)
	}
}

func TestRunBootstrapRejectsConflictingAction(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	wrong := workloadCreateIntent(1, mutation.preparation.Workload, mutation.preparation.Transaction.ID.String())
	wrong.IntentDigest = domain.Hash([]byte("wrong bootstrap intent"))
	_, err := mutation.lock.RecordActionIntent(
		context.Background(),
		mutation.preparation.Transaction.ID,
		wrong,
	)
	if err != nil {
		t.Fatalf("RecordActionIntent() error = %v", err)
	}

	mutation.preparation.Actions, err = state.Actions(context.Background(), mutation.preparation.Transaction.ID)
	if err != nil {
		t.Fatalf("Actions() error = %v", err)
	}
	mutation.preparation.Plan.Kind = PlanResume

	runtime := missingBootstrapRuntime()
	runtime.image = observedImageProbe(mutation.preparation.Workload.Image)

	err = runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, ErrConflictingState) || runtime.creates != 0 || runtime.starts != 0 {
		t.Fatalf("runBootstrap(conflicting action) = %v", err)
	}
}

func TestRunBootstrapRejectsMissingImageBeforeExistingCreate(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	intent := workloadCreateIntent(1, mutation.preparation.Workload, mutation.preparation.Transaction.ID.String())
	_, err := mutation.lock.RecordActionIntent(
		context.Background(),
		mutation.preparation.Transaction.ID,
		intent,
	)
	if err != nil {
		t.Fatalf("RecordActionIntent() error = %v", err)
	}

	mutation.preparation.Actions, err = state.Actions(context.Background(), mutation.preparation.Transaction.ID)
	if err != nil {
		t.Fatalf("Actions() error = %v", err)
	}
	mutation.preparation.Plan.Kind = PlanResume

	err = runBootstrap(
		context.Background(),
		mutation,
		missingBootstrapRuntime(),
		bootstrapCredentials{},
	)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("runBootstrap(missing image) = %v", err)
	}
}

func TestRunBootstrapRejectsInvalidCapabilities(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()

	if !errors.Is(runBootstrap(context.Background(), nil, runtime, bootstrapCredentials{}), ErrInvalidRequest) ||
		!errors.Is(runBootstrap(context.Background(), mutation, nil, bootstrapCredentials{}), ErrInvalidRequest) ||
		!errors.Is(runBootstrap(context.Background(), mutation, runtime, nil), ErrInvalidRequest) {
		t.Fatal("runBootstrap() accepted an invalid capability")
	}

	mutation.preparation.Plan.Kind = PlanRestore
	if !errors.Is(runBootstrap(context.Background(), mutation, runtime, bootstrapCredentials{}), ErrInvalidRequest) {
		t.Fatal("runBootstrap() accepted a restore plan")
	}
}

func TestBootstrapImagePresenceRejectsUnprovenEvidence(t *testing.T) {
	t.Parallel()

	expected := testImageEffectIdentity(t)
	exact := observedImageProbe(expected)
	events := make([]string, 0)

	for _, test := range []struct {
		name  string
		probe ImageProbe
		err   error
	}{
		{name: testProbeErrorName, probe: exact, err: errTestBoundary},
		{name: "missing evidence", probe: ImageProbe{State: ImageProbeMissing, Image: exact.Image}, err: ErrConflictingState},
		{
			name:  "identity mismatch",
			probe: ImageProbe{State: ImageProbeObserved, Image: emptyImageEvidence()},
			err:   ErrConflictingState,
		},
		{
			name:  "unknown state",
			probe: ImageProbe{State: ImageProbeUnknown, Image: emptyImageEvidence()},
			err:   ErrConflictingState,
		},
		{
			name:  "invalid state",
			probe: ImageProbe{State: ImageProbeState(99), Image: emptyImageEvidence()},
			err:   ErrConflictingState,
		},
	} {
		runtime := &testImageRuntime{events: &events, probe: test.probe}
		if test.name == testProbeErrorName {
			runtime.probeErr = test.err
		}

		present, err := imagePresent(context.Background(), runtime, expected)
		if present || !errors.Is(err, test.err) {
			t.Fatalf("imagePresent(%s) = %t, %v", test.name, present, err)
		}
	}
}

func TestBootstrapImageSettlementReturnsProbeFailure(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()
	runtime.imageProbeErrAt[1] = errTestBoundary

	_, _, err := settleBootstrapImage(
		context.Background(),
		mutation,
		runtime,
		bootstrapCredentials{},
	)
	if !errors.Is(err, errTestBoundary) {
		t.Fatalf("settleBootstrapImage() = %v", err)
	}
}

func TestBootstrapSettlersRejectConflictingActions(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	identifier := mutation.preparation.Transaction.ID
	image := imagePullIntent(1, mutation.preparation.Workload.Image)
	image.IntentDigest = domain.Hash([]byte("conflicting pull"))
	imageAction := store.Action{
		TransactionID: identifier,
		Sequence:      image.Sequence,
		Kind:          image.Kind,
		State:         store.ActionStateIntent,
		IntentDigest:  image.IntentDigest,
	}

	err := settleImagePull(
		context.Background(),
		mutation,
		missingBootstrapRuntime(),
		bootstrapCredentials{},
		imageAction,
		1,
	)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("settleImagePull(conflict) = %v", err)
	}

	start := workloadStartIntent(2, mutation.preparation.Workload, identifier.String())
	start.IntentDigest = domain.Hash([]byte("conflicting start"))
	startAction := store.Action{
		TransactionID: identifier,
		Sequence:      start.Sequence,
		Kind:          start.Kind,
		State:         store.ActionStateIntent,
		IntentDigest:  start.IntentDigest,
	}

	err = settleWorkloadStart(context.Background(), mutation, missingBootstrapRuntime(), startAction, 2)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("settleWorkloadStart(conflict) = %v", err)
	}
}

func TestBootstrapValidationRejectsOtherJourneysAndActionOrders(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	mutation.preparation.Transaction.State = store.TransactionFailed
	if validBootstrapMutation(mutation) {
		t.Fatal("validBootstrapMutation() accepted a finished transaction")
	}
	mutation.preparation.Transaction.State = store.TransactionActive

	mutation.preparation.Plan.Kind = PlanResume
	mutation.preparation.Plan.Observation.State = WorkloadObservationPresent
	if validBootstrapMutation(mutation) {
		t.Fatal("validBootstrapMutation() accepted an actionless observed recovery")
	}

	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect
	if validBootstrapMutation(mutation) {
		t.Fatal("validBootstrapMutation() accepted an actionless unknown-effect recovery")
	}

	valid := []store.Action{
		{Kind: imagePullActionKind},
		{Kind: workloadCreateActionKind},
		{Kind: workloadStartActionKind},
	}
	if !validBootstrapActions(valid) {
		t.Fatal("validBootstrapActions() rejected the complete bootstrap journey")
	}

	tooLong := []store.Action{
		{Kind: imagePullActionKind},
		{Kind: workloadCreateActionKind},
		{Kind: workloadStartActionKind},
		{Kind: workloadStartActionKind},
	}
	if validBootstrapActions(tooLong) || validBootstrapActions([]store.Action{{Kind: workloadStartActionKind}}) {
		t.Fatal("validBootstrapActions() accepted an invalid action order")
	}
}

func TestBootstrapCompletionRejectsLostFenceAndMismatchedTransaction(t *testing.T) {
	t.Parallel()

	t.Run("lost fence", func(t *testing.T) {
		t.Parallel()

		state, mutation := newBootstrapMutation(t)
		if err := mutation.lock.Close(); err != nil {
			t.Fatalf("ServiceLock.Close() error = %v", err)
		}

		runtime := missingBootstrapRuntime()
		runtime.workload = WorkloadEffectProbe{
			State: WorkloadEffectProbeObserved,
			Workload: startedWorkloadEffectEvidence(
				mutation.preparation.Workload,
				mutation.preparation.Transaction.ID.String(),
			),
		}

		err := completeBootstrap(context.Background(), mutation, runtime)
		if err == nil {
			t.Fatal("completeBootstrap() accepted a closed service lock")
		}

		mutation.lock = nil
		closeMutationTestStore(t, state)
	})

	t.Run("transaction mismatch", func(t *testing.T) {
		t.Parallel()

		state, mutation := newBootstrapMutation(t)
		defer closeBootstrapMutation(t, state, mutation)

		runtime := missingBootstrapRuntime()
		runtime.workload = WorkloadEffectProbe{
			State: WorkloadEffectProbeObserved,
			Workload: startedWorkloadEffectEvidence(
				mutation.preparation.Workload,
				mutation.preparation.Transaction.ID.String(),
			),
		}
		mutation.preparation.Workload.EffectiveDigest = domain.Hash([]byte("mismatched bootstrap state"))
		err := completeBootstrap(context.Background(), mutation, runtime)
		if !errors.Is(err, ErrConflictingState) {
			t.Fatalf("completeBootstrap(mismatch) = %v", err)
		}
	})
}

func TestBootstrapCompletionReturnsFinalProbeFailure(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	runtime := missingBootstrapRuntime()
	runtime.startProbeErr = errTestBoundary

	err := completeBootstrap(context.Background(), mutation, runtime)
	if !errors.Is(err, errTestBoundary) {
		t.Fatalf("completeBootstrap(probe failure) = %v", err)
	}
}

func TestBootstrapCompletedEffectVerificationRejectsUnprovenEvidence(t *testing.T) {
	t.Parallel()

	state, mutation := newBootstrapMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	preparation := mutation.preparation
	transaction := preparation.Transaction.ID.String()
	evidence := createdWorkloadEffectEvidence(preparation.Workload, transaction)
	digest := workloadObservedEffectDigest(
		workloadEffectObserved,
		workloadCreateActionKind,
		preparation.Workload,
		transaction,
		evidence.ID,
		evidence.StorageDigest,
	)
	action := store.Action{PostconditionDigest: &digest}
	runtime := missingBootstrapRuntime()
	runtime.workload = evidenceProbe(evidence)

	runtime.createProbeErr = errTestBoundary
	err := verifyCompletedWorkloadCreate(context.Background(), action, runtime, preparation)
	if !errors.Is(err, errTestBoundary) {
		t.Fatalf("verifyCompletedWorkloadCreate(probe) = %v", err)
	}

	runtime.createProbeErr = nil
	runtime.workload = WorkloadEffectProbe{State: WorkloadEffectProbeMissing, Workload: emptyWorkloadEffectEvidence()}
	err = verifyCompletedWorkloadCreate(context.Background(), action, runtime, preparation)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("verifyCompletedWorkloadCreate(missing) = %v", err)
	}

	evidence.Lifecycle = WorkloadLifecycleExited
	if createdWorkloadIdentityMatches(evidence, preparation.Workload, transaction) {
		t.Fatal("createdWorkloadIdentityMatches() accepted an exited workload")
	}

	badDigest := domain.Hash([]byte("wrong postcondition"))
	action.PostconditionDigest = &badDigest
	runtime.workload = evidenceProbe(createdWorkloadEffectEvidence(preparation.Workload, transaction))
	err = verifyCompletedWorkloadCreate(context.Background(), action, runtime, preparation)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("verifyCompletedWorkloadCreate(digest) = %v", err)
	}
}

func TestBootstrapArchiveImageNeverPulls(t *testing.T) {
	t.Parallel()

	t.Run("conflicting pull journal", func(t *testing.T) {
		t.Parallel()

		state, mutation := newBootstrapMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		mutation.preparation.Workload.Image.Origin = domain.ImageOriginDockerArchive
		mutation.preparation.Actions = []store.Action{{Kind: imagePullActionKind}}

		_, _, err := settleBootstrapImage(
			context.Background(), mutation, missingBootstrapRuntime(), bootstrapCredentials{},
		)
		if !errors.Is(err, ErrConflictingState) {
			t.Fatalf("settleBootstrapImage(pull journal) error = %v", err)
		}
	})

	t.Run("runtime image disappeared", func(t *testing.T) {
		t.Parallel()

		state, mutation := newBootstrapMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		mutation.preparation.Workload.Image.Origin = domain.ImageOriginDockerArchive

		_, _, err := settleBootstrapImage(
			context.Background(), mutation, missingBootstrapRuntime(), bootstrapCredentials{},
		)
		if !errors.Is(err, ErrArchiveImageMissing) {
			t.Fatalf("settleBootstrapImage(missing archive) error = %v", err)
		}
	})
}

func TestRequireAndVerifySatisfiedEffectRejectInvalidPostconditions(t *testing.T) {
	t.Parallel()

	digest := domain.Hash([]byte("bootstrap postcondition"))
	satisfied := EffectPostcondition{Digest: digest, Satisfied: true}

	unsatisfied := EffectPostcondition{Digest: digest, Satisfied: false}
	if !errors.Is(requireSatisfiedEffect(unsatisfied, nil), ErrConflictingState) ||
		!errors.Is(requireSatisfiedEffect(satisfied, errTestBoundary), errTestBoundary) {
		t.Fatal("requireSatisfiedEffect() accepted an invalid result")
	}

	events := make([]string, 0)
	effect := testRuntimeEffect{events: &events, postcondition: satisfied, probeErr: errTestBoundary}
	action := store.Action{PostconditionDigest: &digest}
	if err := verifyCompletedRuntimeEffect(context.Background(), action, effect); !errors.Is(err, errTestBoundary) {
		t.Fatalf("verifyCompletedRuntimeEffect(probe) = %v", err)
	}

	effect.probeErr = nil
	effect.postcondition.Satisfied = false
	if err := verifyCompletedRuntimeEffect(context.Background(), action, effect); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("verifyCompletedRuntimeEffect(unsatisfied) = %v", err)
	}
}

func evidenceProbe(evidence WorkloadEffectEvidence) WorkloadEffectProbe {
	return WorkloadEffectProbe{State: WorkloadEffectProbeObserved, Workload: evidence}
}

func newBootstrapMutation(t *testing.T) (*store.Store, *boundMutation) {
	t.Helper()

	state := openMutationTestStore(t)
	workload := testWorkloadEffect(t)
	execution := testExecutionEvidence()

	lock, err := state.TryLockService(testProjectName, testServiceName)
	if err != nil {
		closeMutationTestStore(t, state)
		t.Fatalf("TryLockService() error = %v", err)
	}

	transaction, err := lock.BeginTransaction(context.Background(), store.TransactionIntent{
		Kind:            store.TransactionBootstrap,
		Runtime:         execution.Kind,
		SourceDigest:    workload.SourceDigest,
		EffectiveDigest: workload.EffectiveDigest,
		ExecutionDigest: execution.Digest,
	})
	if err != nil {
		closeMutationTestLock(t, lock)
		closeMutationTestStore(t, state)
		t.Fatalf("BeginTransaction() error = %v", err)
	}

	return state, &boundMutation{
		preparation: Preparation{
			Plan: Plan{
				Kind:        PlanBootstrap,
				Project:     testProjectName,
				Service:     testServiceName,
				Runtime:     execution.Kind,
				Platform:    execution.Platform,
				Image:       workload.Image,
				Source:      workload.SourceDigest,
				Desired:     workload.EffectiveDigest,
				Observation: missingObservation(),
			},
			Workload:       workload,
			Execution:      execution,
			Transaction:    transaction,
			HasTransaction: true,
			Actions:        nil,
		},
		lock: lock,
	}
}

func closeBootstrapMutation(t *testing.T, state *store.Store, mutation *boundMutation) {
	t.Helper()

	closeBoundMutation(t, mutation)
	closeMutationTestStore(t, state)
}

func missingBootstrapRuntime() *bootstrapRuntimeFixture {
	return &bootstrapRuntimeFixture{
		image:           ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()},
		workload:        WorkloadEffectProbe{State: WorkloadEffectProbeMissing, Workload: emptyWorkloadEffectEvidence()},
		imageProbeCalls: 0,
		imageProbeErrAt: make(map[int]error),
		createProbeErr:  nil,
		startProbeErr:   nil,
		pulls:           0,
		creates:         0,
		starts:          0,
	}
}

func observedImageProbe(expected domain.ImageIdentity) ImageProbe {
	return ImageProbe{
		State: ImageProbeObserved,
		Image: ImageEvidence{
			ReferenceDigest:  expected.ReferenceDigest,
			PlatformManifest: expected.PlatformManifest,
			ImageConfig:      expected.ImageConfig,
			Platform:         expected.Platform,
		},
	}
}

func assertBootstrapActions(
	t *testing.T,
	state *store.Store,
	identifier store.TransactionID,
	want []string,
) {
	t.Helper()

	actions, err := state.Actions(context.Background(), identifier)
	if err != nil || len(actions) != len(want) {
		t.Fatalf("Actions() = %#v, %v", actions, err)
	}

	for index, kind := range want {
		if actions[index].Sequence != int64(index+1) || actions[index].Kind != kind ||
			actions[index].State != store.ActionStateCompleted || actions[index].PostconditionDigest == nil {
			t.Fatalf("action %d = %#v, want %q completed", index, actions[index], kind)
		}
	}
}

func assertCompletedBootstrap(
	t *testing.T,
	state *store.Store,
	mutation *boundMutation,
) {
	t.Helper()

	transaction, found, err := state.UnresolvedTransaction(
		context.Background(),
		mutation.preparation.Plan.Project,
		mutation.preparation.Plan.Service,
	)
	if err != nil || found || transaction != (store.Transaction{}) {
		t.Fatalf("UnresolvedTransaction() = %#v, %t, %v", transaction, found, err)
	}

	applied, found, err := state.AppliedService(
		context.Background(),
		mutation.preparation.Plan.Project,
		mutation.preparation.Plan.Service,
	)
	if err != nil || !found || applied != mutation.preparation.Applied || !mutation.preparation.HasApplied {
		t.Fatalf("AppliedService() = %#v, %t, %v", applied, found, err)
	}
}
