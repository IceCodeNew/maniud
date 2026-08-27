package containerd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tasktypes "github.com/containerd/containerd/api/types/task"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestWorkloadExtensionRoundTripAndStrictDecoding(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	configuration := testContainerdConfiguration(t, desired)
	extension := workloadExtensionV1{
		Version: containerExtensionVersion, Configuration: configuration,
		ImageReference:   desired.Image.Reference,
		ImageConfig:      desired.Image.ImageConfig.String(),
		PlatformManifest: desired.Image.PlatformManifest.String(),
		RuntimeMounts: []domain.RuntimeMount{{
			Kind: domain.MountBind, Source: testSourcePath, Target: "/target", ReadOnly: true,
		}},
		RuntimeSpecDigest: domain.Hash([]byte("runtime")).String(),
		SnapshotParent:    domain.Hash([]byte("snapshot")).String(),
		NetworkDigest:     domain.Hash([]byte("network")).String(),
	}
	encoded, err := encodeWorkloadExtension(extension)
	if err != nil {
		t.Fatalf("encodeWorkloadExtension() error = %v", err)
	}
	decoded, err := decodeWorkloadExtension(encoded)
	if err != nil || !reflect.DeepEqual(decoded, extension) {
		t.Fatalf("decodeWorkloadExtension() = %#v, %v", decoded, err)
	}

	invalid := []*anypb.Any{
		nil,
		{TypeUrl: "wrong", Value: encoded.GetValue()},
		{TypeUrl: containerConfigurationTypeURL},
		{TypeUrl: containerConfigurationTypeURL, Value: []byte("{")},
		{TypeUrl: containerConfigurationTypeURL, Value: append(slices.Clone(encoded.GetValue()), '\n')},
		{TypeUrl: containerConfigurationTypeURL, Value: []byte(`{"version":1,"unknown":true}`)},
		{TypeUrl: containerConfigurationTypeURL, Value: bytes.Repeat([]byte("x"), maximumContainerExtensionBytes+1)},
	}
	for _, value := range invalid {
		if _, decodeErr := decodeWorkloadExtension(value); !errors.Is(decodeErr, ErrProtocol) {
			t.Fatalf("decodeWorkloadExtension(%#v) error = %v", value, decodeErr)
		}
	}

	wrongVersion := extension
	wrongVersion.Version++
	wrong, err := encodeWorkloadExtension(wrongVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeWorkloadExtension(wrong); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeWorkloadExtension(version) error = %v", err)
	}
	oversized := extension
	oversized.RuntimeMounts = make([]domain.RuntimeMount, maximumContainerExtensionBytes/4)
	if _, err = encodeWorkloadExtension(oversized); !errors.Is(err, ErrProtocol) {
		t.Fatalf("encodeWorkloadExtension(oversized) error = %v", err)
	}
}

func TestRuntimeSpecEvidenceIsCanonicalAndBounded(t *testing.T) {
	t.Parallel()

	configuration := testContainerdConfiguration(t, testContainerdDesiredWorkload(t))
	encoded, digest, err := encodeRuntimeSpec(configuration)
	if err != nil || digest == (domain.Digest{}) {
		t.Fatalf("encodeRuntimeSpec() = %#v, %v, %v", encoded, digest, err)
	}
	got, err := runtimeSpecDigest(encoded)
	if err != nil || got != digest {
		t.Fatalf("runtimeSpecDigest() = %v, %v", got, err)
	}
	for _, value := range []*anypb.Any{
		nil,
		{TypeUrl: "wrong", Value: encoded.GetValue()},
		{TypeUrl: containerRuntimeSpecTypeURL},
		{TypeUrl: containerRuntimeSpecTypeURL, Value: []byte("{")},
		{TypeUrl: containerRuntimeSpecTypeURL, Value: bytes.Repeat([]byte("x"), maximumContainerExtensionBytes+1)},
	} {
		if _, digestErr := runtimeSpecDigest(value); !errors.Is(digestErr, ErrProtocol) {
			t.Fatalf("runtimeSpecDigest(%#v) error = %v", value, digestErr)
		}
	}
}

//nolint:cyclop // The table verifies every complete, partial, and malformed ownership shape.
func TestWorkloadLabelsOwnershipAndRestartPolicy(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	labels := workloadLabels(desired, testWorkloadTransaction)
	ownership := parseWorkloadOwnership(labels)
	if !ownership.Matches(
		desired.ServiceName, testWorkloadTransaction, desired.EffectiveDigest, desired.Image.ReferenceDigest,
	) || ownership.ImageConfig != desired.Image.ImageConfig ||
		ownership.PlatformManifest != desired.Image.PlatformManifest {
		t.Fatalf("parseWorkloadOwnership() = %#v", ownership)
	}
	if got := parseWorkloadOwnership(nil); got.Status != domain.OwnershipUnmanaged {
		t.Fatalf("parseWorkloadOwnership(nil) = %#v", got)
	}
	got := parseWorkloadOwnership(map[string]string{containerNameLabel: testWorkloadService})
	if got.Status != domain.OwnershipConflicting {
		t.Fatalf("parseWorkloadOwnership(maniud) = %#v", got)
	}
	for _, key := range []string{
		domain.LabelService,
		domain.LabelTransaction,
		domain.LabelDesiredStateDigest,
		domain.LabelReferenceDigest,
		domain.LabelImageConfigDigest,
		domain.LabelPlatformManifestDigest,
	} {
		partial := cloneStringLabels(labels)
		delete(partial, key)
		if got := parseWorkloadOwnership(partial); got.Status != domain.OwnershipConflicting {
			t.Fatalf("parseWorkloadOwnership(missing %q) = %#v", key, got)
		}
	}
	malformed := cloneStringLabels(labels)
	malformed[domain.LabelDesiredStateDigest] = testBadValue
	if got := parseWorkloadOwnership(malformed); got.Status != domain.OwnershipConflicting {
		t.Fatalf("parseWorkloadOwnership(malformed) = %#v", got)
	}
	if cloneStringLabels(nil) != nil {
		t.Fatal("cloneStringLabels(nil) returned a map")
	}
	clone := cloneStringLabels(labels)
	clone[domain.LabelService] = testChangedValue
	if labels[domain.LabelService] == testChangedValue {
		t.Fatal("cloneStringLabels() shared storage")
	}

	if restartLabels("") != nil || restartLabels("no") != nil {
		t.Fatal("restartLabels(disabled) returned labels")
	}
	if got := restartLabels(testRestartPolicy); got[containerdRestartPolicyLabel] != testRestartPolicy ||
		got[containerdRestartStatusLabel] != containerdRestartDesiredStopped {
		t.Fatalf("restartLabels(always) = %#v", got)
	}
}

func TestTaskLifecycleMapsEveryProtocolState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status tasktypes.Status
		found  bool
		want   application.WorkloadLifecycle
		valid  bool
	}{
		{0, false, application.WorkloadLifecycleCreated, true},
		{tasktypes.Status_CREATED, true, application.WorkloadLifecycleCreated, true},
		{tasktypes.Status_RUNNING, true, application.WorkloadLifecycleRunning, true},
		{tasktypes.Status_STOPPED, true, application.WorkloadLifecycleExited, true},
		{tasktypes.Status_PAUSED, true, application.WorkloadLifecyclePaused, true},
		{tasktypes.Status_UNKNOWN, true, application.WorkloadLifecycleUnknown, false},
		{tasktypes.Status_PAUSING, true, application.WorkloadLifecycleUnknown, false},
		{tasktypes.Status(255), true, application.WorkloadLifecycleUnknown, false},
	}
	for _, test := range tests {
		got, err := taskLifecycle(test.status, test.found)
		if got != test.want || (err == nil) != test.valid {
			t.Fatalf("taskLifecycle(%v, %v) = %v, %v", test.status, test.found, got, err)
		}
	}
}

//nolint:cyclop,funlen // One matrix keeps the complete adapter input policy auditable.
func TestContainerdWorkloadPolicyAndEvidenceHelpers(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	if !validContainerdWorkload(desired) || !validContainerdImage(desired.Image) ||
		!validContainerdPlatform(desired.Platform) || (&Client{}).CheckWorkload(desired) == nil {
		t.Fatal("valid workload policy rejected or empty client accepted")
	}
	client := &Client{workloads: &fakeWorkloadBackend{}}
	if err := client.CheckWorkload(desired); err != nil {
		t.Fatalf("CheckWorkload(valid) = %v", err)
	}
	withUserLabel := desired
	withUserLabel.Labels = []string{"example=value"}
	withUserLabel.EffectiveDigest = domain.ComputeEffectiveDigest(withUserLabel)
	if !validContainerdWorkload(withUserLabel) {
		t.Fatal("validContainerdWorkload(user label) rejected")
	}
	withProtectedLabel := desired
	withProtectedLabel.Labels = []string{domain.LabelService + "=bad"}
	withProtectedLabel.EffectiveDigest = domain.ComputeEffectiveDigest(withProtectedLabel)
	if validContainerdWorkload(withProtectedLabel) {
		t.Fatal("validContainerdWorkload(protected label) accepted")
	}
	mutations := []func(*domain.DesiredWorkload){
		func(value *domain.DesiredWorkload) { value.Image.Reference = "bad" },
		func(value *domain.DesiredWorkload) { value.Image.ReferenceDigest = domain.Digest{} },
		func(value *domain.DesiredWorkload) { value.Image.PlatformManifest = domain.Digest{} },
		func(value *domain.DesiredWorkload) { value.Image.ImageConfig = domain.Digest{} },
		func(value *domain.DesiredWorkload) { value.Image.Origin = domain.ImageOriginDockerArchive },
		func(value *domain.DesiredWorkload) { value.Platform.Architecture = containerdArchitectureARM64 },
		func(value *domain.DesiredWorkload) { value.SourceDigest = domain.Digest{} },
		func(value *domain.DesiredWorkload) { value.EffectiveDigest = domain.Digest{} },
		func(value *domain.DesiredWorkload) { value.Labels = []string{testMissingValue} },
		func(value *domain.DesiredWorkload) { value.Labels = []string{domain.LabelService + "=bad"} },
		func(value *domain.DesiredWorkload) { value.Labels = []string{containerNameLabel + "=bad"} },
		func(value *domain.DesiredWorkload) { value.Labels = []string{containerdRestartPolicyLabel + "=always"} },
	}
	for _, mutate := range mutations {
		invalid := desired
		mutate(&invalid)
		if validContainerdWorkload(invalid) || client.CheckWorkload(invalid) == nil {
			t.Fatalf("invalid workload accepted: %#v", invalid)
		}
	}

	arm := domain.Platform{OS: containerdPlatformOS, Architecture: containerdArchitectureARM64, Variant: "v8"}
	if !validContainerdPlatform(arm) || validContainerdPlatform(domain.Platform{OS: "darwin", Architecture: "arm64"}) ||
		validContainerdPlatform(domain.Platform{OS: containerdPlatformOS, Architecture: containerdArchitectureARM64}) {
		t.Fatal("validContainerdPlatform() policy drift")
	}
	info := workloadRuntimeInfo{
		Platform: desired.Platform, Runtime: "runc", Snapshotter: "overlayfs",
		NetworkDigest: domain.Hash([]byte("network")), Restart: true,
	}
	if !validWorkloadRuntimeInfo(info) || validWorkloadRuntimeInfo(workloadRuntimeInfo{}) {
		t.Fatal("validWorkloadRuntimeInfo() policy drift")
	}
	first := containerdExecutionDigest(runtimeScope{version: "1", process: 1, pidns: 2}, info)
	info.Restart = false
	second := containerdExecutionDigest(runtimeScope{version: "1", process: 1, pidns: 2}, info)
	if first == second || bytes.Equal(appendContainerdString(nil, "ab"), appendContainerdString(nil, "a")) {
		t.Fatal("containerd evidence digest failed to bind fields")
	}
}

//nolint:cyclop // The table covers each stable public and private error class.
func TestWorkloadEvidenceAndErrorClassification(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	evidence, err := workloadEffectEvidence(native, desired)
	if err != nil || !validEffectWorkload(evidence, desired, testWorkloadTransaction) ||
		!validEffectStorage(evidence, desired) {
		t.Fatalf("workloadEffectEvidence() = %#v, %v", evidence, err)
	}
	if _, err = workloadEffectEvidence(nil, desired); !errors.Is(err, ErrProtocol) {
		t.Fatalf("workloadEffectEvidence(nil) = %v", err)
	}
	invalidStorage := desired
	invalidStorage.Mounts = []domain.Mount{{Kind: domain.MountVolume, Target: testStateMount}}
	if _, err = workloadEffectEvidence(native, invalidStorage); !errors.Is(err, ErrProtocol) {
		t.Fatalf("workloadEffectEvidence(storage) = %v", err)
	}
	if got := missingWorkloadObservation(); got.State != application.WorkloadObservationMissing || got.ID != "" {
		t.Fatalf("missingWorkloadObservation() = %#v", got)
	}
	for _, selected := range []error{
		nil, context.Canceled, context.DeadlineExceeded, ErrUnavailable, ErrProtocol,
		ErrUnsupportedWorkload, application.ErrArchivePathMissing, application.ErrArchiveConflict,
	} {
		if got := workloadError(selected); !errors.Is(got, selected) {
			t.Fatalf("workloadError(%v) = %v", selected, got)
		}
	}
	if got := workloadError(errContainerdTest); !errors.Is(got, ErrProtocol) {
		t.Fatalf("workloadError(private) = %v", got)
	}
	if wrapWorkloadBackendError("read", nil) != nil ||
		!errors.Is(wrapWorkloadBackendError("read", errContainerdTest), errContainerdTest) {
		t.Fatal("wrapWorkloadBackendError() did not preserve error identity")
	}
}

//nolint:cyclop,funlen // The table covers every path, name, option, and transition boundary.
func TestWorkloadOptionsPathsNamesAndTransitions(t *testing.T) {
	t.Parallel()

	options := DefaultWorkloadOptions()
	if !options.valid() || options.CNINetworkName != defaultCNINetworkName {
		t.Fatalf("DefaultWorkloadOptions() = %#v", options)
	}
	invalidOptions := make([]WorkloadOptions, 1, 5)
	invalid := options
	invalid.StateRoot = testRelativePath
	invalidOptions = append(invalidOptions, invalid)
	invalid = options
	invalid.CNIPluginDirectories = nil
	invalidOptions = append(invalidOptions, invalid)
	invalid = options
	invalid.Runtime = " bad "
	invalidOptions = append(invalidOptions, invalid)
	invalid = options
	invalid.Snapshotter = "bad value"
	invalidOptions = append(invalidOptions, invalid)
	for _, value := range invalidOptions {
		if value.valid() {
			t.Fatalf("WorkloadOptions.valid() accepted %#v", value)
		}
	}
	for _, path := range []string{"/", "/state/file"} {
		if !validArchivePath(path) {
			t.Fatalf("validArchivePath(%q) rejected", path)
		}
	}
	for _, path := range []string{"", testRelativePath, "/bad/../path", "/bad\x00path"} {
		if validArchivePath(path) {
			t.Fatalf("validArchivePath(%q) accepted", path)
		}
	}
	if !validTransaction(testWorkloadTransaction) || validTransaction("") ||
		validTransaction(strings.Repeat("x", 257)) || validTransaction("bad\x00value") {
		t.Fatal("validTransaction() policy drift")
	}
	if !validContainerdID(testWorkloadName) || validContainerdID("-api") ||
		validContainerdID(strings.Repeat("x", 65)) || validContainerdName("bad/name") ||
		!asciiAlphaNumeric('A') || !asciiAlphaNumeric('9') || asciiAlphaNumeric('_') {
		t.Fatal("containerd name policy drift")
	}

	desired := testContainerdDesiredWorkload(t)
	native := testManagedNativeWorkload(t, desired)
	before := existingWorkloadProbe(native).Workload
	before.Lifecycle = application.WorkloadLifecycleRunning
	transition := application.WorkloadTransition{
		Kind: application.WorkloadTransitionStop, Before: before, After: before,
	}
	transition.After.Lifecycle = application.WorkloadLifecycleExited
	if !validContainerdTransition(transition) {
		t.Fatalf("validContainerdTransition(%#v) rejected", transition)
	}
	transition.Before.ID = testBadIdentifier
	if validContainerdTransition(transition) {
		t.Fatal("validContainerdTransition(invalid ID) accepted")
	}
}

func TestCandidateSelectionAndDeterministicNames(t *testing.T) {
	t.Parallel()

	workload := &nativeWorkload{ID: "id", Ownership: domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged}}
	managed := &nativeWorkload{ID: "id", Ownership: domain.WorkloadOwnership{Status: domain.OwnershipManaged}}
	tests := []struct {
		candidates workloadCandidates
		found      bool
		consistent bool
	}{
		{workloadCandidates{}, false, true},
		{workloadCandidates{Named: workload}, true, true},
		{workloadCandidates{Owned: managed}, true, true},
		{workloadCandidates{Named: managed}, false, false},
		{workloadCandidates{Named: workload, Owned: managed}, true, true},
		{workloadCandidates{Named: workload, Owned: &nativeWorkload{ID: testOtherValue}}, false, false},
	}
	for _, test := range tests {
		_, found, consistent := selectWorkloadCandidate(test.candidates)
		if found != test.found || consistent != test.consistent {
			t.Fatalf("selectWorkloadCandidate(%#v) = %v, %v", test.candidates, found, consistent)
		}
	}
	identifier := workloadIdentifier("api", testWorkloadTransaction)
	if identifier != workloadIdentifier("api", testWorkloadTransaction) ||
		identifier == workloadIdentifier(testOtherValue, testWorkloadTransaction) ||
		workloadSnapshotKey(identifier) != identifier+workloadSnapshotSuffix {
		t.Fatal("deterministic workload identifiers drifted")
	}
	if labels := userContainerLabels([]string{"a=1", "b=two"}); !reflect.DeepEqual(
		labels, map[string]string{"a": "1", "b": "two"},
	) {
		t.Fatalf("userContainerLabels() = %#v", labels)
	}
}

func TestArchivePathStatCapturesFileMetadata(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := archivePathStat(info, "target")
	if got.Name != testFileName || got.Size != 5 || got.Mode.Perm() != 0o600 ||
		got.LinkTarget != "target" || got.ModTime.Equal(time.Time{}) {
		t.Fatalf("archivePathStat() = %#v", got)
	}
}

type fakeWorkloadBackend struct {
	info              workloadRuntimeInfo
	candidates        workloadCandidates
	workload          *nativeWorkload
	removalCandidate  *nativeWorkload
	available         bool
	err               error
	workloadErr       error
	nameErr           error
	removalErr        error
	archiveErr        error
	createID          string
	createRequest     createWorkloadRequest
	stat              application.ArchivePathStat
	created           int
	started           int
	stopped           int
	renamed           int
	removed           int
	archiveGet        int
	archivePut        int
	removalIncomplete bool
}

func (backend *fakeWorkloadBackend) Inspect(context.Context) (workloadRuntimeInfo, error) {
	return backend.info, backend.err
}

func (backend *fakeWorkloadBackend) Candidates(
	context.Context,
	string,
	string,
	string,
) (workloadCandidates, error) {
	return backend.candidates, backend.err
}

func (backend *fakeWorkloadBackend) Workload(context.Context, string) (*nativeWorkload, error) {
	if backend.workloadErr != nil {
		return nil, backend.workloadErr
	}

	return backend.workload, backend.err
}

func (backend *fakeWorkloadBackend) RemovalCandidate(context.Context, string) (*nativeWorkload, error) {
	if backend.workloadErr != nil {
		return nil, backend.workloadErr
	}

	return backend.removalCandidate, backend.err
}

func (backend *fakeWorkloadBackend) NameAvailable(context.Context, string, string) (bool, error) {
	if backend.nameErr != nil {
		return false, backend.nameErr
	}

	return backend.available, backend.err
}

func (backend *fakeWorkloadBackend) RemovalComplete(context.Context, string) (bool, error) {
	if backend.removalErr != nil {
		return false, backend.removalErr
	}

	return !backend.removalIncomplete, backend.err
}

func (backend *fakeWorkloadBackend) Create(_ context.Context, request createWorkloadRequest) (string, error) {
	backend.created++
	backend.createRequest = request

	return backend.createID, backend.err
}

func (backend *fakeWorkloadBackend) Start(context.Context, string) error {
	backend.started++

	return backend.err
}

func (backend *fakeWorkloadBackend) Stop(context.Context, string, time.Duration) error {
	backend.stopped++

	return backend.err
}

func (backend *fakeWorkloadBackend) Rename(context.Context, string, string) error {
	backend.renamed++

	return backend.err
}

func (backend *fakeWorkloadBackend) Remove(context.Context, string, bool) error {
	backend.removed++

	return backend.err
}

func (backend *fakeWorkloadBackend) ArchiveStat(
	context.Context,
	string,
	string,
) (application.ArchivePathStat, error) {
	if backend.archiveErr != nil {
		return application.ArchivePathStat{}, backend.archiveErr
	}

	return backend.stat, backend.err
}

func (backend *fakeWorkloadBackend) ArchiveGet(
	context.Context,
	string,
	string,
	io.Writer,
	int64,
) (application.ArchivePathStat, error) {
	backend.archiveGet++
	if backend.archiveErr != nil {
		return application.ArchivePathStat{}, backend.archiveErr
	}

	return backend.stat, backend.err
}

func (backend *fakeWorkloadBackend) ArchivePut(context.Context, string, string, io.Reader) error {
	backend.archivePut++
	if backend.archiveErr != nil {
		return backend.archiveErr
	}

	return backend.err
}
