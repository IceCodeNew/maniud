package application

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
	"github.com/IceCodeNew/maniud/internal/store"
)

type upgradeRuntimeFixture struct {
	bootstrapRuntimeFixture

	archives          map[string][]byte
	putRewrite        map[string][]byte
	getCalls          int
	putCalls          int
	getErrAt          map[int]error
	lastCreateOptions WorkloadCreateOptions
	lastCreated       domain.DesiredWorkload
	predecessor       ExistingWorkload
	predecessorHealth WorkloadHealth
	transitionProbes  int
	transitionApplies map[WorkloadTransitionKind]int
	transitionProbeAt map[int]error
	transitionApply   map[WorkloadTransitionKind]error
	transitionSkip    map[WorkloadTransitionKind]bool
	pullUnchanged     bool
	pullApplyErr      error
	createMissing     bool
	createApplyErr    error
	startUnchanged    bool
	startApplyErr     error
	startProbeCalls   int
	startProbeErrAt   map[int]error
	discards          int
	discardApplyErr   error
	discardProbeErr   error
}

func (runtime *upgradeRuntimeFixture) ProbeStartedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (WorkloadEffectProbe, error) {
	runtime.startProbeCalls++
	if runtime.startProbeErrAt[runtime.startProbeCalls] != nil {
		return WorkloadEffectProbe{}, runtime.startProbeErrAt[runtime.startProbeCalls]
	}

	return runtime.bootstrapRuntimeFixture.ProbeStartedWorkload(ctx, workload, transaction)
}

func (runtime *upgradeRuntimeFixture) PullImage(
	ctx context.Context,
	expected domain.ImageIdentity,
	authenticator credential.Provider,
) error {
	if runtime.pullApplyErr != nil {
		return runtime.pullApplyErr
	}
	if runtime.pullUnchanged {
		runtime.pulls++

		return nil
	}

	return runtime.bootstrapRuntimeFixture.PullImage(ctx, expected, authenticator)
}

func (runtime *upgradeRuntimeFixture) CreateWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	options WorkloadCreateOptions,
) (string, error) {
	if runtime.createApplyErr != nil {
		return "", runtime.createApplyErr
	}

	runtime.lastCreateOptions = options
	runtime.lastCreated = workload
	if runtime.createMissing {
		runtime.creates++

		return testWorkloadEffectID, nil
	}

	return runtime.bootstrapRuntimeFixture.CreateWorkload(ctx, workload, transaction, options)
}

func (runtime *upgradeRuntimeFixture) ApplyWorkloadTransition(
	_ context.Context,
	transition WorkloadTransition,
) error {
	runtime.transitionApplies[transition.Kind]++
	if runtime.transitionApply[transition.Kind] != nil {
		return runtime.transitionApply[transition.Kind]
	}
	if runtime.transitionSkip[transition.Kind] {
		return nil
	}
	if runtime.predecessor != transition.Before {
		return ErrConflictingState
	}

	runtime.predecessor = transition.After

	return nil
}

func (runtime *upgradeRuntimeFixture) StartWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) error {
	if runtime.startApplyErr != nil {
		return runtime.startApplyErr
	}
	if runtime.startUnchanged {
		runtime.starts++

		return nil
	}

	return runtime.bootstrapRuntimeFixture.StartWorkload(ctx, workload, transaction)
}

func (runtime *upgradeRuntimeFixture) ProbeWorkloadTransition(
	_ context.Context,
	_ WorkloadTransition,
) (WorkloadTransitionProbe, error) {
	runtime.transitionProbes++
	if err := runtime.transitionProbeAt[runtime.transitionProbes]; err != nil {
		return WorkloadTransitionProbe{}, err
	}

	if runtime.predecessor == (ExistingWorkload{}) {
		return WorkloadTransitionProbe{State: WorkloadEffectProbeMissing}, nil
	}
	health := runtime.predecessorHealth
	if !validWorkloadHealth(health) {
		health = WorkloadHealth{Status: WorkloadHealthAbsent}
	}

	return WorkloadTransitionProbe{
		State:    WorkloadEffectProbeObserved,
		Workload: runtime.predecessor,
		Health:   health,
	}, nil
}

func (runtime *upgradeRuntimeFixture) ProbeWorkloadArchivePath(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
	path string,
) (ArchivePathStat, error) {
	return ArchivePathStat{Name: path, Mode: os.ModeDir, ModTime: time.Unix(1, 0)}, nil
}

func (runtime *upgradeRuntimeFixture) GetWorkloadArchive(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
	path string,
	destination io.Writer,
	_ int64,
) (ArchivePathStat, error) {
	runtime.getCalls++
	if err := runtime.getErrAt[runtime.getCalls]; err != nil {
		return ArchivePathStat{}, err
	}
	raw, found := runtime.archives[path]
	if !found {
		return ArchivePathStat{}, ErrArchivePathMissing
	}

	_, err := destination.Write(raw)
	if err != nil {
		return ArchivePathStat{}, fmt.Errorf("write archive fixture: %w", err)
	}

	return ArchivePathStat{Name: path, Size: int64(len(raw))}, nil
}

func (runtime *upgradeRuntimeFixture) PutWorkloadArchive(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
	path string,
	source io.Reader,
) error {
	runtime.putCalls++
	raw, err := io.ReadAll(source)
	if err != nil {
		return fmt.Errorf("read archive fixture: %w", err)
	}

	if runtime.archives == nil {
		runtime.archives = map[string][]byte{}
	}
	if rewritten, found := runtime.putRewrite[path]; found {
		runtime.archives[path] = rewritten

		return nil
	}

	runtime.archives[path] = raw

	return nil
}

func upgradeTestArchive(t *testing.T, body string) []byte {
	t.Helper()

	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	header := &tar.Header{Name: testDataName, Mode: 0o640, Size: int64(len(body)), ModTime: time.Unix(1, 0)}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := io.WriteString(writer, body); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close tar: %v", err)
	}

	return output.Bytes()
}

func (runtime *upgradeRuntimeFixture) DiscardWorkload(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
) error {
	runtime.discards++
	if runtime.discardApplyErr != nil {
		return runtime.discardApplyErr
	}
	if runtime.workload.State == WorkloadEffectProbeMissing {
		return ErrConflictingState
	}

	runtime.workload = WorkloadEffectProbe{
		State:    WorkloadEffectProbeMissing,
		Workload: emptyWorkloadEffectEvidence(),
	}

	return nil
}

func (runtime *upgradeRuntimeFixture) ProbeDiscardedWorkload(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
) (WorkloadEffectProbe, error) {
	return runtime.workload, runtime.discardProbeErr
}

func TestRunUpgradeReplacesAndCommitsExactPredecessor(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil {
		t.Fatalf("runUpgrade() error = %v", err)
	}

	if mutation.preparation.Transaction.State != store.TransactionSucceeded ||
		runtime.predecessor != (ExistingWorkload{}) || runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("upgrade result = transaction %#v, predecessor %#v, create %d, start %d",
			mutation.preparation.Transaction,
			runtime.predecessor,
			runtime.creates,
			runtime.starts,
		)
	}

	assertBootstrapActions(t, state, mutation.preparation.Transaction.ID, []string{
		workloadStopActionKind,
		workloadRenameActionKind,
		workloadCreateActionKind,
		workloadStartActionKind,
		workloadRemoveActionKind,
	})
	assertCompletedBootstrap(t, state, mutation)
}

func TestRunUpgradeCopiesWritableVolumeBeforeStart(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	archive := upgradeTestArchive(t, "payload")
	attachUpgradeVolume(mutation, runtime, archive)
	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("runUpgrade(volume) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}

	assertVolumeUpgradeCommitted(t, state, mutation, runtime, archive)
	if runtime.lastCreateOptions.CopyImageVolumes {
		t.Fatal("volume replacement copied image content")
	}
}

func TestRunUpgradeCopiesWritableBindOnProvenanceChange(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	archive := upgradeTestArchive(t, "bind-payload")
	runtime.archives = map[string][]byte{testVolumeTarget: archive}
	mutation.preparation.Workload.Mounts = []domain.Mount{{
		Kind: domain.MountBind, Source: testBindSourceNew, Target: testVolumeTarget,
	}}
	mutation.preparation.Plan.Observation.RuntimeMounts = []domain.RuntimeMount{{
		Kind: domain.MountBind, Source: testBindSourceOld, Target: testVolumeTarget,
	}}

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("runUpgrade(bind) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}
	if !runtime.lastCreateOptions.CopyImageVolumes {
		t.Fatal("bind replacement disabled image volume copy")
	}

	wantSource, err := replacementBindPath(
		mutation.backupRoot,
		mutation.preparation.Transaction.ID.String(),
		domain.Mount{Target: testVolumeTarget},
	)
	if err != nil || runtime.lastCreated.Mounts[0].Source != wantSource {
		t.Fatalf("replacement bind source = %#v, %v", runtime.lastCreated.Mounts, err)
	}
	info, err := os.Lstat(wantSource)
	if err != nil || !info.IsDir() {
		t.Fatalf("replacement bind directory = %#v, %v", info, err)
	}
	entries, err := os.ReadDir(wantSource)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replacement bind contents = %#v, %v", entries, err)
	}

	assertBootstrapActions(t, state, mutation.preparation.Transaction.ID, []string{
		storageInventoryActionKind,
		workloadStopActionKind,
		storageBackupActionKind,
		workloadRenameActionKind,
		workloadCreateActionKind,
		storageRestoreActionKind,
		workloadStartActionKind,
		workloadRemoveActionKind,
	})
}

func TestRunUpgradeResumesCompletedVolumeBackupWithoutRepublish(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	archive := upgradeTestArchive(t, "payload")
	attachUpgradeVolume(mutation, runtime, archive)
	runtime.transitionProbeAt[2] = errTestBoundary

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || mutation.preparation.Transaction.State != store.TransactionActive {
		t.Fatalf("runUpgrade(unknown rename after backup) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}

	publication, found, err := backup.Open(
		context.Background(),
		mutation.backupRoot,
		backup.Identifier(mutation.preparation.Transaction.ID),
	)
	if err != nil || !found || publication.ManifestDigest == (domain.Digest{}) {
		t.Fatalf("Open(first backup) = %#v, %t, %v", publication, found, err)
	}

	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	delete(runtime.transitionProbeAt, 2)
	getsBefore := runtime.getCalls
	putsBefore := runtime.putCalls

	err = runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("runUpgrade(resume backup) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}

	assertResumedBackupPublication(t, mutation, publication)
	if runtime.putCalls != putsBefore+1 {
		t.Fatalf("restore replayed extra PUTs: before %d after %d", putsBefore, runtime.putCalls)
	}
	if runtime.getCalls <= getsBefore {
		t.Fatal("resume skipped restore verification GET")
	}

	assertVolumeUpgradeCommitted(t, state, mutation, runtime, archive)
}

func assertResumedBackupPublication(t *testing.T, mutation *boundMutation, published backup.Publication) {
	t.Helper()

	reopened, found, err := backup.Open(
		context.Background(),
		mutation.backupRoot,
		backup.Identifier(mutation.preparation.Transaction.ID),
	)
	if err != nil || !found || reopened.ManifestDigest != published.ManifestDigest ||
		reopened.Manifest.OperationToken != published.Manifest.OperationToken ||
		reopened.Manifest.CreatedUnix != published.Manifest.CreatedUnix {
		t.Fatalf("Open(resumed backup) = %#v, %t, %v", reopened, found, err)
	}
}

func TestRunUpgradeProbesUnknownVolumeRestoreWithoutReplay(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	archive := upgradeTestArchive(t, "payload")
	attachUpgradeVolume(mutation, runtime, archive)
	runtime.getErrAt = map[int]error{3: errTestBoundary}

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || mutation.preparation.Transaction.State != store.TransactionActive {
		t.Fatalf("runUpgrade(unknown restore) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}

	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	delete(runtime.getErrAt, 3)
	putsBefore := runtime.putCalls

	err = runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("runUpgrade(unknown restore) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}
	if runtime.putCalls != putsBefore {
		t.Fatalf("unknown restore replayed PUT: before %d after %d", putsBefore, runtime.putCalls)
	}

	assertVolumeUpgradeCommitted(t, state, mutation, runtime, archive)
}

func TestRunUpgradeResumesCompletedVolumeInventory(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	archive := upgradeTestArchive(t, "payload")
	attachUpgradeVolume(mutation, runtime, archive)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err := runUpgrade(cancelled, mutation, runtime, bootstrapCredentials{})
	if err == nil {
		t.Fatal("runUpgrade(cancelled) succeeded")
	}

	mutation.preparation.Plan.Kind = PlanResume
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	err = runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("runUpgrade(resume) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}
}

func TestRunUpgradeRecoversUnknownVolumeBackupWithoutReplay(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	archive := upgradeTestArchive(t, "payload")
	attachUpgradeVolume(mutation, runtime, archive)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err := runUpgrade(cancelled, mutation, runtime, bootstrapCredentials{})
	if err == nil || mutation.preparation.Transaction.State != store.TransactionActive {
		t.Fatalf("runUpgrade(cancelled) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}

	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	err = runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("runUpgrade(recovered backup) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}
}

func TestRunUpgradeRejectsRestoredVolumeDigestMismatch(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	attachUpgradeVolume(mutation, runtime, upgradeTestArchive(t, "original"))
	runtime.putRewrite = map[string][]byte{testVolumeTarget: upgradeTestArchive(t, "changed")}

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, ErrConflictingState) ||
		mutation.preparation.Transaction.State != store.TransactionDegraded ||
		runtime.starts != 0 {
		t.Fatalf("runUpgrade(mismatch) = %v, transaction %#v, starts %d",
			err, mutation.preparation.Transaction, runtime.starts)
	}
}

func TestRunUpgradeRevalidatesRuntimeSourceImmediatelyBeforeCreate(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	checks := 0
	mutation.materialize = func() error {
		checks++
		if checks == 2 {
			return errTestBoundary
		}

		return nil
	}

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || checks != 2 || runtime.creates != 0 ||
		mutation.preparation.Transaction.State != store.TransactionActive {
		t.Fatalf("runUpgrade(materialize drift) = %v, checks %d, creates %d, transaction %#v",
			err, checks, runtime.creates, mutation.preparation.Transaction)
	}
	actions := readUpgradeActions(t, state, mutation)
	if len(actions) != 2 || actions[0].Kind != workloadStopActionKind ||
		actions[1].Kind != workloadRenameActionKind {
		t.Fatalf("materialize drift actions = %#v", actions)
	}
}

func TestRunUpgradeRejectsRuntimeSourceDriftBeforeTransitions(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	mutation.materialize = func() error { return errTestBoundary }

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || len(runtime.transitionApplies) != 0 || runtime.creates != 0 {
		t.Fatalf("runUpgrade(early materialize drift) = %v, transitions %#v, creates %d",
			err, runtime.transitionApplies, runtime.creates)
	}
}

func TestRunUpgradeRecoversUnknownTransitionWithoutReplay(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.transitionProbeAt[1] = errTestBoundary

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, errTestBoundary) || runtime.transitionApplies[WorkloadTransitionStop] != 1 ||
		mutation.preparation.Transaction.State != store.TransactionActive {
		t.Fatalf("runUpgrade(unknown stop) = %v, applies %#v, transaction %#v",
			err,
			runtime.transitionApplies,
			mutation.preparation.Transaction,
		)
	}

	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	delete(runtime.transitionProbeAt, 1)

	err = runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err != nil || runtime.transitionApplies[WorkloadTransitionStop] != 1 ||
		mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("runUpgrade(recovered stop) = %v, applies %#v, transaction %#v",
			err,
			runtime.transitionApplies,
			mutation.preparation.Transaction,
		)
	}
}

func TestRunRestoreDiscardsReplacementAndRestartsPredecessor(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.createMissing = true

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, ErrConflictingState) ||
		mutation.preparation.Transaction.State != store.TransactionDegraded {
		t.Fatalf("runUpgrade(create failure) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}

	mutation.preparation.Plan.Kind = PlanRestore
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	err = runRestore(context.Background(), mutation, runtime)
	if err != nil || mutation.preparation.Transaction.State != store.TransactionFailed ||
		runtime.predecessor != newUpgradeJourney(mutation.preparation).stop.Before || runtime.discards != 1 {
		t.Fatalf("runRestore() = %v, transaction %#v, predecessor %#v, discards %d",
			err,
			mutation.preparation.Transaction,
			runtime.predecessor,
			runtime.discards,
		)
	}
}

func TestUpgradeAndRestoreRejectInvalidCapabilities(t *testing.T) {
	t.Parallel()

	if !errors.Is(runUpgrade(context.Background(), nil, nil, bootstrapCredentials{}), ErrInvalidRequest) ||
		!errors.Is(runRestore(context.Background(), nil, nil), ErrInvalidRequest) {
		t.Fatal("upgrade orchestration accepted nil capabilities")
	}

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	mutation.preparation.Plan.Kind = PlanBootstrap

	if !errors.Is(runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{}), ErrInvalidRequest) {
		t.Fatal("runUpgrade() accepted a bootstrap plan")
	}
}

func assertVolumeUpgradeCommitted(
	t *testing.T,
	state *store.Store,
	mutation *boundMutation,
	runtime *upgradeRuntimeFixture,
	archive []byte,
) {
	t.Helper()

	if runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("volume upgrade effects = create %d, start %d", runtime.creates, runtime.starts)
	}

	assertBootstrapActions(t, state, mutation.preparation.Transaction.ID, []string{
		storageInventoryActionKind,
		workloadStopActionKind,
		storageBackupActionKind,
		workloadRenameActionKind,
		workloadCreateActionKind,
		storageRestoreActionKind,
		workloadStartActionKind,
		workloadRemoveActionKind,
	})
	assertCompletedBootstrap(t, state, mutation)

	index, found, err := state.BackupIndex(context.Background(), mutation.preparation.Transaction.ID)
	if err != nil || !found || index.ManifestDigest == (domain.Digest{}) {
		t.Fatalf("BackupIndex() = %#v, %t, %v", index, found, err)
	}

	publication, found, err := backup.Open(
		context.Background(),
		mutation.backupRoot,
		backup.Identifier(mutation.preparation.Transaction.ID),
	)
	if err != nil || !found || publication.ManifestDigest != index.ManifestDigest {
		t.Fatalf("Open(backup) = %#v, %t, %v", publication, found, err)
	}
	if !bytes.Equal(runtime.archives[testVolumeTarget], archive) {
		t.Fatalf("restored archive drifted: %q", runtime.archives[testVolumeTarget])
	}
}

func attachUpgradeVolume(mutation *boundMutation, runtime *upgradeRuntimeFixture, archive []byte) {
	runtime.archives = map[string][]byte{testVolumeTarget: archive}
	mutation.preparation.Workload.Mounts = []domain.Mount{{
		Kind: domain.MountVolume, Target: testVolumeTarget,
	}}
	mutation.preparation.Plan.Observation.RuntimeMounts = []domain.RuntimeMount{{
		Kind: domain.MountVolume, Name: testVolumeName, Source: testVolumeSource, Target: testVolumeTarget,
	}}
}

func newUpgradeMutation(t *testing.T) (*store.Store, *boundMutation, *upgradeRuntimeFixture) {
	t.Helper()

	state := openMutationTestStore(t)
	workload := testWorkloadEffect(t)
	execution := testExecutionEvidence()
	previous := workload
	previous.SourceDigest = domain.Hash([]byte("previous source"))
	previous.EffectiveDigest = domain.Hash([]byte("previous desired state"))
	applied := seedAppliedMutation(t, state, previous, execution)

	lock, err := state.TryLockService(testProjectName, testServiceName)
	if err != nil {
		closeMutationTestStore(t, state)
		t.Fatalf("TryLockService(upgrade) error = %v", err)
	}

	transaction, err := lock.BeginTransaction(context.Background(), store.TransactionIntent{
		Kind:                  store.TransactionUpgrade,
		Runtime:               execution.Kind,
		SourceDigest:          workload.SourceDigest,
		EffectiveDigest:       workload.EffectiveDigest,
		ExecutionDigest:       execution.Digest,
		BaseTransactionID:     applied.TransactionID,
		HasBaseTransaction:    true,
		PredecessorWorkloadID: applied.WorkloadID,
	})
	if err != nil {
		closeMutationTestLock(t, lock)
		closeMutationTestStore(t, state)
		t.Fatalf("BeginTransaction(upgrade) error = %v", err)
	}

	preparation := newUpgradePreparation(workload, execution, transaction, applied)
	journey := newUpgradeJourney(preparation)
	root, err := state.BackupRoot()
	if err != nil {
		closeMutationTestLock(t, lock)
		closeMutationTestStore(t, state)
		t.Fatalf("BackupRoot(upgrade) error = %v", err)
	}

	return state, &boundMutation{
		preparation: preparation, lock: lock, backupRoot: root,
	}, newUpgradeRuntime(workload, journey)
}

func newUpgradePreparation(
	workload domain.DesiredWorkload,
	execution RuntimeEvidence,
	transaction store.Transaction,
	applied store.AppliedService,
) Preparation {
	return Preparation{
		Plan: Plan{
			Kind:        PlanUpgrade,
			Project:     testProjectName,
			Service:     testServiceName,
			Runtime:     execution.Kind,
			Platform:    execution.Platform,
			Image:       workload.Image,
			Source:      workload.SourceDigest,
			Desired:     workload.EffectiveDigest,
			Observation: appliedMutationObservation(workload, applied, false),
		},
		Workload:       workload,
		Execution:      execution,
		Transaction:    transaction,
		HasTransaction: true,
		Applied:        applied,
		HasApplied:     true,
	}
}

func TestUpgradeArchiveImageNeverPulls(t *testing.T) {
	t.Parallel()

	t.Run("conflicting pull journal", func(t *testing.T) {
		t.Parallel()

		state, mutation, runtime := newUpgradeMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		mutation.preparation.Workload.Image.Origin = domain.ImageOriginDockerArchive
		mutation.preparation.Actions = []store.Action{{Kind: imagePullActionKind}}

		_, _, _, err := settleUpgradeImage(
			context.Background(), mutation, runtime, bootstrapCredentials{},
		)
		if !errors.Is(err, ErrConflictingState) {
			t.Fatalf("settleUpgradeImage(pull journal) error = %v", err)
		}
	})

	t.Run("runtime image disappeared", func(t *testing.T) {
		t.Parallel()

		state, mutation, runtime := newUpgradeMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		mutation.preparation.Workload.Image.Origin = domain.ImageOriginDockerArchive
		runtime.image = ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()}

		_, _, _, err := settleUpgradeImage(
			context.Background(), mutation, runtime, bootstrapCredentials{},
		)
		if !errors.Is(err, ErrArchiveImageMissing) {
			t.Fatalf("settleUpgradeImage(missing archive) error = %v", err)
		}
	})
}

func newUpgradeRuntime(workload domain.DesiredWorkload, journey upgradeJourney) *upgradeRuntimeFixture {
	return &upgradeRuntimeFixture{
		image:             observedImageProbe(workload.Image),
		workload:          WorkloadEffectProbe{State: WorkloadEffectProbeMissing, Workload: emptyWorkloadEffectEvidence()},
		imageProbeCalls:   0,
		imageProbeErrAt:   make(map[int]error),
		createProbeErr:    nil,
		startProbeErr:     nil,
		pulls:             0,
		creates:           0,
		starts:            0,
		predecessor:       journey.stop.Before,
		transitionApplies: make(map[WorkloadTransitionKind]int),
		transitionProbeAt: make(map[int]error),
		transitionApply:   make(map[WorkloadTransitionKind]error),
		transitionSkip:    make(map[WorkloadTransitionKind]bool),
		startProbeErrAt:   make(map[int]error),
	}
}

func readUpgradeActions(
	t *testing.T,
	state *store.Store,
	mutation *boundMutation,
) []store.Action {
	t.Helper()

	actions, err := state.Actions(context.Background(), mutation.preparation.Transaction.ID)
	if err != nil {
		t.Fatalf("Actions(upgrade) error = %v", err)
	}

	return actions
}

var _ credential.Provider = bootstrapCredentials{}
