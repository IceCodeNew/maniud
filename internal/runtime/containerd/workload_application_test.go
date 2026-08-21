package containerd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	"github.com/opencontainers/go-digest"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestApplicationCreateUsesVerifiedSnapshotParent(t *testing.T) {
	t.Parallel()

	identity, image, content := testContainerdImageGraph(t, []byte("layer"))
	desired := testContainerdDesiredWorkload(t)
	desired.Image = identity
	desired.Platform = identity.Platform
	desired.EffectiveDigest = domain.ComputeEffectiveDigest(desired)
	backend := &fakeWorkloadBackend{createID: "created-id"}
	client := testCheckedWorkloadClient(t, backend)
	client.images = fakeImagesClient{response: &imagesapi.GetImageResponse{Image: image}}
	client.content = content
	identifier, err := client.CreateWorkload(
		context.Background(), desired, testWorkloadTransaction,
		application.WorkloadCreateOptions{CopyImageVolumes: true},
	)
	if err != nil || identifier != backend.createID || backend.created != 1 ||
		backend.createRequest.SnapshotParent != digest.FromBytes([]byte("layer")).String() ||
		!backend.createRequest.CopyImageVolumes {
		t.Fatalf("CreateWorkload() = %q, %v, calls %d", identifier, err, backend.created)
	}
	if _, err = client.CreateWorkload(
		context.Background(), desired, "bad\x00transaction", application.WorkloadCreateOptions{},
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("CreateWorkload(invalid) = %v", err)
	}
	invalid := desired
	invalid.ServiceName = ""
	if _, err = client.CreateWorkload(
		context.Background(), invalid, testWorkloadTransaction, application.WorkloadCreateOptions{},
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("CreateWorkload(invalid configuration) = %v", err)
	}
	client.images = fakeImagesClient{err: errContainerdTest}
	if _, err = client.CreateWorkload(
		context.Background(), desired, testWorkloadTransaction, application.WorkloadCreateOptions{},
	); err == nil {
		t.Fatal("CreateWorkload(image probe failure) succeeded")
	}
}

//nolint:cyclop // The test covers valid, missing, conflicting, malformed, and unavailable observations.
func TestApplicationInspectAndObserveUseCheckedBackendEvidence(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	info := workloadRuntimeInfo{
		Platform: desired.Platform, Runtime: defaultContainerdRuntime,
		Snapshotter:   defaultContainerdSnapshotter,
		NetworkDigest: domain.Hash([]byte("network")), Restart: true,
	}
	backend := &fakeWorkloadBackend{info: info, candidates: workloadCandidates{Named: native}}
	client := testCheckedWorkloadClient(t, backend)
	evidence, err := client.Inspect(context.Background())
	if err != nil || evidence.Kind != domain.RuntimeContainerd || evidence.Platform != desired.Platform ||
		evidence.Digest == (domain.Digest{}) {
		t.Fatalf("Inspect() = %#v, %v", evidence, err)
	}
	observation, err := client.ObserveWorkload(context.Background(), desired)
	if err != nil || observation.State != application.WorkloadObservationPresent ||
		observation.ID != native.ID || !observation.ConfigurationMatches || observation.Running {
		t.Fatalf("ObserveWorkload() = %#v, %v", observation, err)
	}

	backend.candidates = workloadCandidates{}
	observation, err = client.ObserveWorkload(context.Background(), desired)
	if err != nil || !reflect.DeepEqual(observation, missingWorkloadObservation()) {
		t.Fatalf("ObserveWorkload(missing) = %#v, %v", observation, err)
	}
	backend.candidates = workloadCandidates{Owned: native}
	if _, err = client.ObserveWorkload(context.Background(), desired); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ObserveWorkload(owned) = %v", err)
	}
	backend.candidates = workloadCandidates{Named: &nativeWorkload{}}
	if _, err = client.ObserveWorkload(context.Background(), desired); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ObserveWorkload(malformed) = %v", err)
	}
	backend.err = ErrUnavailable
	if _, err = client.Inspect(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Inspect(failure) = %v", err)
	}
	if _, err = client.ObserveWorkload(context.Background(), desired); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ObserveWorkload(failure) = %v", err)
	}
	invalid := desired
	invalid.SourceDigest = domain.Digest{}
	if _, err = client.ObserveWorkload(context.Background(), invalid); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ObserveWorkload(invalid) = %v", err)
	}
	backend.err = nil
	backend.info = workloadRuntimeInfo{}
	if _, err = client.Inspect(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Inspect(malformed) = %v", err)
	}
	if _, err = (&Client{}).Inspect(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Inspect(empty) = %v", err)
	}
	if err = client.PullImage(context.Background(), desired.Image, nil); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("PullImage() = %v", err)
	}
}

//nolint:cyclop // The test covers the full create, start, and discard probe contract.
func TestWorkloadEffectProbeCreateStartAndDiscard(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	backend := &fakeWorkloadBackend{candidates: workloadCandidates{Named: native, Owned: native}}
	client := testCheckedWorkloadClient(t, backend)

	probe, err := client.ProbeCreatedWorkload(
		context.Background(), desired, testWorkloadTransaction, native.ID,
	)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved ||
		!validEffectWorkload(probe.Workload, desired, testWorkloadTransaction) {
		t.Fatalf("ProbeCreatedWorkload() = %#v, %v", probe, err)
	}
	if err = client.StartWorkload(context.Background(), desired, testWorkloadTransaction); err != nil ||
		backend.started != 1 {
		t.Fatalf("StartWorkload() = %v, calls %d", err, backend.started)
	}
	probe, err = client.ProbeStartedWorkload(context.Background(), desired, testWorkloadTransaction)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved {
		t.Fatalf("ProbeStartedWorkload() = %#v, %v", probe, err)
	}
	if err = client.DiscardWorkload(context.Background(), desired, testWorkloadTransaction); err != nil ||
		backend.removed != 1 {
		t.Fatalf("DiscardWorkload() = %v, calls %d", err, backend.removed)
	}
	probe, err = client.ProbeDiscardedWorkload(context.Background(), desired, testWorkloadTransaction)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved {
		t.Fatalf("ProbeDiscardedWorkload() = %#v, %v", probe, err)
	}

	backend.candidates = workloadCandidates{}
	probe, err = client.ProbeCreatedWorkload(context.Background(), desired, testWorkloadTransaction, "")
	if err != nil || probe.State != application.WorkloadEffectProbeMissing ||
		!reflect.DeepEqual(probe.Workload, emptyWorkloadEvidence()) {
		t.Fatalf("ProbeCreatedWorkload(missing) = %#v, %v", probe, err)
	}
	backend.candidates = workloadCandidates{Named: native, Owned: &nativeWorkload{ID: testOtherValue}}
	if _, err = client.ProbeCreatedWorkload(
		context.Background(), desired, testWorkloadTransaction, "",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(conflict) = %v", err)
	}
	backend.candidates = workloadCandidates{Named: native, Owned: native}
	if _, err = client.ProbeCreatedWorkload(
		context.Background(), desired, testWorkloadTransaction, "different",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(response drift) = %v", err)
	}
	if _, err = client.ProbeCreatedWorkload(
		context.Background(), desired, "bad\x00transaction", "",
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeCreatedWorkload(invalid) = %v", err)
	}
}

//nolint:cyclop // The operation table verifies all four native transition contracts.
func TestWorkloadTransitionsApplyAndProbe(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	native.Lifecycle = application.WorkloadLifecycleRunning
	before := existingWorkloadProbe(native).Workload
	stopped := before
	stopped.Lifecycle = application.WorkloadLifecycleExited
	renamed := stopped
	renamed.Name = testRenamedWorkloadName
	transitions := []application.WorkloadTransition{
		{Kind: application.WorkloadTransitionStop, Before: before, After: stopped},
		{Kind: application.WorkloadTransitionRename, Before: stopped, After: renamed},
		{Kind: application.WorkloadTransitionRemove, Before: renamed},
		{Kind: application.WorkloadTransitionRestoreStart, Before: stopped, After: before},
	}
	for _, transition := range transitions {
		observed := transition.Before
		workload := nativeFromExisting(observed)
		backend := &fakeWorkloadBackend{workload: workload, available: true}
		client := testCheckedWorkloadClient(t, backend)
		if err := client.ApplyWorkloadTransition(context.Background(), transition); err != nil {
			t.Fatalf("ApplyWorkloadTransition(%v) = %v", transition.Kind, err)
		}
		calls := backend.started + backend.stopped + backend.renamed + backend.removed
		if calls != 1 {
			t.Fatalf("ApplyWorkloadTransition(%v) calls = %d", transition.Kind, calls)
		}

		if transition.Kind == application.WorkloadTransitionRemove {
			backend.workload = nil
			probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
			if err != nil || probe.State != application.WorkloadEffectProbeMissing {
				t.Fatalf("ProbeWorkloadTransition(remove) = %#v, %v", probe, err)
			}

			continue
		}
		backend.workload = nativeFromExisting(transition.After)
		probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
		if err != nil || probe.State != application.WorkloadEffectProbeObserved ||
			probe.Workload != transition.After {
			t.Fatalf("ProbeWorkloadTransition(%v) = %#v, %v", transition.Kind, probe, err)
		}
	}

	invalid := transitions[0]
	invalid.Before.ID = testBadIdentifier
	client := testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
	if err := client.ApplyWorkloadTransition(context.Background(), invalid); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ApplyWorkloadTransition(invalid) = %v", err)
	}
	if _, err := client.ProbeWorkloadTransition(context.Background(), invalid); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeWorkloadTransition(invalid) = %v", err)
	}
}

func TestRemovalProbeRejectsIncompleteCleanup(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	native.Lifecycle = application.WorkloadLifecycleExited
	transition := application.WorkloadTransition{
		Kind: application.WorkloadTransitionRemove, Before: existingWorkloadProbe(native).Workload,
	}
	backend := &fakeWorkloadBackend{available: true, removalIncomplete: true}
	client := testCheckedWorkloadClient(t, backend)
	if _, err := client.ProbeWorkloadTransition(
		context.Background(), transition,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadTransition(incomplete removal) = %v", err)
	}
}

func nativeFromExisting(existing application.ExistingWorkload) *nativeWorkload {
	return &nativeWorkload{
		ID: existing.ID, Name: existing.Name, ConfigurationDigest: existing.ConfigurationDigest,
		Lifecycle: existing.Lifecycle, Ownership: existing.Ownership,
	}
}

//nolint:cyclop // The test covers valid archive operations and each fail-closed input boundary.
func TestWorkloadArchiveApplicationContract(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	stat := application.ArchivePathStat{Name: "state", Size: 4, Mode: 0o600}
	backend := &fakeWorkloadBackend{
		candidates: workloadCandidates{Named: native, Owned: native}, stat: stat,
	}
	client := testCheckedWorkloadClient(t, backend)
	got, err := client.ProbeWorkloadArchivePath(
		context.Background(), desired, testWorkloadTransaction, "/state",
	)
	if err != nil || got != stat {
		t.Fatalf("ProbeWorkloadArchivePath() = %#v, %v", got, err)
	}
	var destination bytes.Buffer
	got, err = client.GetWorkloadArchive(
		context.Background(), desired, testWorkloadTransaction, "/state", &destination, 1024,
	)
	if err != nil || got != stat || backend.archiveGet != 1 {
		t.Fatalf("GetWorkloadArchive() = %#v, %v, calls %d", got, err, backend.archiveGet)
	}
	if err = client.PutWorkloadArchive(
		context.Background(), desired, testWorkloadTransaction, "/state", bytes.NewReader(nil),
	); err != nil || backend.archivePut != 1 {
		t.Fatalf("PutWorkloadArchive() = %v, calls %d", err, backend.archivePut)
	}

	backend.candidates = workloadCandidates{}
	if _, err = client.ProbeWorkloadArchivePath(
		context.Background(), desired, testWorkloadTransaction, "/state",
	); !errors.Is(err, application.ErrArchiveConflict) {
		t.Fatalf("ProbeWorkloadArchivePath(missing) = %v", err)
	}
	if _, err = client.GetWorkloadArchive(
		context.Background(), desired, testWorkloadTransaction, "/state", nil, 0,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("GetWorkloadArchive(invalid) = %v", err)
	}
	if err = client.PutWorkloadArchive(
		context.Background(), desired, testWorkloadTransaction, "/state", nil,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("PutWorkloadArchive(invalid) = %v", err)
	}
	if _, err = client.ProbeWorkloadArchivePath(
		context.Background(), desired, testWorkloadTransaction, "relative",
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeWorkloadArchivePath(invalid) = %v", err)
	}
}

func TestConfigurationDigestRejectsInvalidProjection(t *testing.T) {
	t.Parallel()

	if got := containerdConfigurationDigest(nativeWorkload{}); got != (domain.Digest{}) {
		t.Fatalf("containerdConfigurationDigest(invalid) = %v", got)
	}
}

func TestWorkloadEffectRejectsProbeAndLifecycleFailures(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	backend := &fakeWorkloadBackend{err: errContainerdTest}
	client := testCheckedWorkloadClient(t, backend)
	if err := client.StartWorkload(
		context.Background(), desired, testWorkloadTransaction,
	); err == nil {
		t.Fatal("StartWorkload(probe failure) succeeded")
	}
	if err := client.DiscardWorkload(
		context.Background(), desired, testWorkloadTransaction,
	); err == nil {
		t.Fatal("DiscardWorkload(probe failure) succeeded")
	}
	backend.err = nil
	backend.candidates = workloadCandidates{Named: native, Owned: native}
	native.Lifecycle = application.WorkloadLifecycleRunning
	if err := client.StartWorkload(
		context.Background(), desired, testWorkloadTransaction,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("StartWorkload(running) = %v", err)
	}
	backend.candidates = workloadCandidates{}
	if err := client.DiscardWorkload(
		context.Background(), desired, testWorkloadTransaction,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("DiscardWorkload(missing) = %v", err)
	}
}

func TestWorkloadCandidateSelectionVariants(t *testing.T) {
	t.Parallel()

	managed := &nativeWorkload{ID: testWorkloadName, Ownership: domain.WorkloadOwnership{Status: domain.OwnershipManaged}}
	unmanaged := &nativeWorkload{
		ID: testOtherValue, Ownership: domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged},
	}
	selected, found, consistent := selectWorkloadCandidate(workloadCandidates{Owned: managed})
	if selected != managed || !found || !consistent {
		t.Fatalf("selectWorkloadCandidate(owned) = %#v, %v, %v", selected, found, consistent)
	}
	selected, found, consistent = selectWorkloadCandidate(workloadCandidates{Named: unmanaged})
	if selected != unmanaged || !found || !consistent {
		t.Fatalf("selectWorkloadCandidate(unmanaged) = %#v, %v, %v", selected, found, consistent)
	}
	if _, _, consistent = selectWorkloadCandidate(workloadCandidates{Named: managed}); consistent {
		t.Fatal("selectWorkloadCandidate(unowned managed) accepted")
	}
}

//nolint:cyclop,funlen // The matrix keeps every transition failure at the transaction boundary.
func TestWorkloadTransitionFailureMatrix(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	native.Lifecycle = application.WorkloadLifecycleExited
	before := existingWorkloadProbe(native).Workload
	after := before
	after.Name = testRenamedWorkloadName
	transition := application.WorkloadTransition{
		Kind: application.WorkloadTransitionRename, Before: before, After: after,
	}
	backend := &fakeWorkloadBackend{workloadErr: errContainerdTest, available: true}
	client := testCheckedWorkloadClient(t, backend)
	if err := client.ApplyWorkloadTransition(context.Background(), transition); err == nil {
		t.Fatal("ApplyWorkloadTransition(read failure) succeeded")
	}
	backend.workloadErr = nil
	if err := client.ApplyWorkloadTransition(
		context.Background(), transition,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ApplyWorkloadTransition(missing) = %v", err)
	}
	backend.workload = nativeFromExisting(before)
	backend.available = false
	if err := client.ApplyWorkloadTransition(
		context.Background(), transition,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ApplyWorkloadTransition(name occupied) = %v", err)
	}
	backend.workload = nil
	probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
	if err != nil || probe.State != application.WorkloadEffectProbeMissing {
		t.Fatalf("ProbeWorkloadTransition(missing) = %#v, %v", probe, err)
	}
	backend.workload = nativeFromExisting(after)
	if _, err = client.ProbeWorkloadTransition(
		context.Background(), transition,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadTransition(old name occupied) = %v", err)
	}
	backend.available = true
	backend.nameErr = errContainerdTest
	if err = client.applyWorkloadTransition(context.Background(), transition); !errors.Is(err, errContainerdTest) {
		t.Fatalf("applyWorkloadTransition(name probe failure) = %v", err)
	}
	if _, err = client.ProbeWorkloadTransition(context.Background(), transition); err == nil {
		t.Fatal("ProbeWorkloadTransition(name probe failure) succeeded")
	}
	backend.nameErr = nil
	backend.workloadErr = errContainerdTest
	if _, err = client.ProbeWorkloadTransition(context.Background(), transition); err == nil {
		t.Fatal("ProbeWorkloadTransition(workload failure) succeeded")
	}
	if err = client.applyWorkloadTransition(
		context.Background(), application.WorkloadTransition{Kind: application.WorkloadTransitionUnknown},
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("applyWorkloadTransition(unknown) = %v", err)
	}
	if err = client.applyWorkloadTransition(context.Background(), application.WorkloadTransition{
		Kind: application.WorkloadTransitionKind(255),
	}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("applyWorkloadTransition(invalid) = %v", err)
	}
}

func TestRemovalProbeReadFailureMatrix(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	native.Lifecycle = application.WorkloadLifecycleExited
	transition := application.WorkloadTransition{
		Kind: application.WorkloadTransitionRemove, Before: existingWorkloadProbe(native).Workload,
	}
	backend := &fakeWorkloadBackend{workloadErr: errContainerdTest, available: true}
	client := testCheckedWorkloadClient(t, backend)
	if _, err := client.ProbeWorkloadTransition(context.Background(), transition); err == nil {
		t.Fatal("ProbeWorkloadTransition(ID failure) succeeded")
	}
	backend.workloadErr = nil
	backend.nameErr = errContainerdTest
	if _, err := client.ProbeWorkloadTransition(context.Background(), transition); err == nil {
		t.Fatal("ProbeWorkloadTransition(name failure) succeeded")
	}
	backend.nameErr = nil
	backend.removalErr = errContainerdTest
	if _, err := client.ProbeWorkloadTransition(context.Background(), transition); err == nil {
		t.Fatal("ProbeWorkloadTransition(cleanup failure) succeeded")
	}
	backend.removalErr = nil
	backend.available = false
	if _, err := client.ProbeWorkloadTransition(
		context.Background(), transition,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadTransition(name residue) = %v", err)
	}
	backend.available = true
	backend.workload = native
	probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved {
		t.Fatalf("ProbeWorkloadTransition(existing) = %#v, %v", probe, err)
	}
}

func TestWorkloadArchiveBackendFailures(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	backend := &fakeWorkloadBackend{
		candidates: workloadCandidates{Named: native, Owned: native}, archiveErr: errContainerdTest,
	}
	client := testCheckedWorkloadClient(t, backend)
	if _, err := client.ProbeWorkloadArchivePath(
		context.Background(), desired, testWorkloadTransaction, testStateMount,
	); err == nil {
		t.Fatal("ProbeWorkloadArchivePath(backend failure) succeeded")
	}
	if _, err := client.GetWorkloadArchive(
		context.Background(), desired, testWorkloadTransaction, testStateMount, &bytes.Buffer{}, 1024,
	); err == nil {
		t.Fatal("GetWorkloadArchive(backend failure) succeeded")
	}
	if err := client.PutWorkloadArchive(
		context.Background(), desired, testWorkloadTransaction, testStateMount, bytes.NewReader(nil),
	); err == nil {
		t.Fatal("PutWorkloadArchive(backend failure) succeeded")
	}
	backend.archiveErr = nil
	backend.err = errContainerdTest
	if _, err := client.GetWorkloadArchive(
		context.Background(), desired, testWorkloadTransaction, testStateMount, &bytes.Buffer{}, 1024,
	); err == nil {
		t.Fatal("GetWorkloadArchive(candidate failure) succeeded")
	}
	if err := client.PutWorkloadArchive(
		context.Background(), desired, testWorkloadTransaction, testStateMount, bytes.NewReader(nil),
	); err == nil {
		t.Fatal("PutWorkloadArchive(candidate failure) succeeded")
	}
}
