package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const testWorkloadEffectID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type testWorkloadEffectRuntime struct {
	events          *[]string
	createID        string
	createErr       error
	probe           WorkloadEffectProbe
	probeErr        error
	created         domain.DesiredWorkload
	transaction     string
	probeResponseID string
	createInvoked   bool
}

type testWorkloadStartRuntime struct {
	events       *[]string
	startErr     error
	probe        WorkloadEffectProbe
	probeErr     error
	started      domain.DesiredWorkload
	transaction  string
	startInvoked bool
}

func (runtime *testWorkloadEffectRuntime) CreateWorkload(
	_ context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	_ WorkloadCreateOptions,
) (string, error) {
	*runtime.events = append(*runtime.events, eventEffect)
	runtime.createInvoked = true
	runtime.created = workload
	runtime.transaction = transaction

	return runtime.createID, runtime.createErr
}

func (runtime *testWorkloadEffectRuntime) ProbeCreatedWorkload(
	_ context.Context,
	_ domain.DesiredWorkload,
	transaction string,
	responseID string,
) (WorkloadEffectProbe, error) {
	*runtime.events = append(*runtime.events, eventProbe)
	runtime.transaction = transaction
	runtime.probeResponseID = responseID

	return runtime.probe, runtime.probeErr
}

func (runtime *testWorkloadStartRuntime) StartWorkload(
	_ context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) error {
	*runtime.events = append(*runtime.events, eventEffect)
	runtime.startInvoked = true
	runtime.started = workload
	runtime.transaction = transaction

	return runtime.startErr
}

func (runtime *testWorkloadStartRuntime) ProbeStartedWorkload(
	_ context.Context,
	_ domain.DesiredWorkload,
	transaction string,
) (WorkloadEffectProbe, error) {
	*runtime.events = append(*runtime.events, eventProbe)
	runtime.transaction = transaction

	return runtime.probe, runtime.probeErr
}

//nolint:cyclop // The assertions keep the complete fenced create sequence in one audit surface.
func TestRunWorkloadCreateFencesAndCompletesObservedCreate(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := observedWorkloadEffectRuntime(&journal.events, workload, identifier.String())

	got, err := runWorkloadCreate(
		context.Background(), journal, identifier, 1, workload, runtime, defaultWorkloadCreateOptions(),
	)
	if err != nil || !got.Satisfied || got.Digest == (domain.Digest{}) {
		t.Fatalf("runWorkloadCreate() = %#v, %v", got, err)
	}

	if !runtime.createInvoked || !reflect.DeepEqual(runtime.created, workload) ||
		runtime.transaction != identifier.String() ||
		runtime.probeResponseID != testWorkloadEffectID {
		t.Fatal("runWorkloadCreate() did not bind create and probe evidence")
	}

	if journal.action.Kind != workloadCreateActionKind ||
		journal.action.IntentDigest != workloadEffectDigest(
			workloadEffectIntent,
			workloadCreateActionKind,
			workload,
			identifier.String(),
			"",
		) {
		t.Fatalf("workload create intent = %#v", journal.action)
	}

	if !equalEvents(journal.events, newEffectEvents()) {
		t.Fatalf("events = %q, want %q", journal.events, newEffectEvents())
	}
}

func TestRunWorkloadCreateAcceptsImportedArchiveIdentity(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	workload.Image.Origin = domain.ImageOriginDockerArchive
	workload.Image.Reference = "example.com/team/api:archive"
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := observedWorkloadEffectRuntime(&journal.events, workload, identifier.String())

	got, err := runWorkloadCreate(
		context.Background(), journal, identifier, 1, workload, runtime, defaultWorkloadCreateOptions(),
	)
	if err != nil || !got.Satisfied || !runtime.createInvoked {
		t.Fatalf("runWorkloadCreate(archive) = %#v, %v", got, err)
	}
}

func TestRunWorkloadStartFencesAndCompletesObservedStart(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := observedWorkloadStartRuntime(&journal.events, workload, identifier.String())

	got, err := runWorkloadStart(context.Background(), journal, identifier, 2, workload, runtime)
	if err != nil || !got.Satisfied || got.Digest == (domain.Digest{}) {
		t.Fatalf("runWorkloadStart() = %#v, %v", got, err)
	}

	if !runtime.startInvoked || !reflect.DeepEqual(runtime.started, workload) ||
		runtime.transaction != identifier.String() {
		t.Fatal("runWorkloadStart() did not bind start and probe evidence")
	}

	if journal.action.Kind != workloadStartActionKind ||
		journal.action.IntentDigest != workloadStartIntent(2, workload, identifier.String()).IntentDigest {
		t.Fatalf("workload start intent = %#v", journal.action)
	}

	if !equalEvents(journal.events, newEffectEvents()) {
		t.Fatalf("events = %q, want %q", journal.events, newEffectEvents())
	}
}

func TestRunWorkloadStartRecoveryProbesWithoutReplay(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateEffectOutcomeUnknown)
	runtime := observedWorkloadStartRuntime(&journal.events, workload, identifier.String())

	got, err := runWorkloadStart(context.Background(), journal, identifier, 2, workload, runtime)
	if err != nil || !got.Satisfied || runtime.startInvoked {
		t.Fatalf("runWorkloadStart(unknown) = %#v, %v", got, err)
	}

	wantEvents := []string{eventIntent, eventProbe, eventComplete}
	if !equalEvents(journal.events, wantEvents) {
		t.Fatalf("events = %q, want %q", journal.events, wantEvents)
	}
}

func TestRunWorkloadStartCompletesProvenAbsenceBeforeFailing(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := &testWorkloadStartRuntime{
		events:   &journal.events,
		startErr: errTestBoundary,
		probe: WorkloadEffectProbe{
			State:    WorkloadEffectProbeMissing,
			Workload: emptyWorkloadEffectEvidence(),
		},
		probeErr:     nil,
		started:      emptyDesiredWorkload(),
		transaction:  "",
		startInvoked: false,
	}

	got, err := runWorkloadStart(context.Background(), journal, identifier, 2, workload, runtime)
	if !errors.Is(err, errTestBoundary) || got.Satisfied || got.Digest == (domain.Digest{}) ||
		journal.action.State != store.ActionStateCompleted {
		t.Fatalf("runWorkloadStart(missing) = %#v, %v", got, err)
	}
}

//nolint:funlen // The table keeps every invalid start postcondition under one assertion.
func TestRunWorkloadStartRejectsUnprovenPostconditions(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	exact := startedWorkloadEffectEvidence(workload, identifier.String())

	tests := []struct {
		name     string
		probe    WorkloadEffectProbe
		probeErr error
		want     error
		resolved bool
	}{
		{
			name:     testProbeFailureName,
			probe:    WorkloadEffectProbe{State: WorkloadEffectProbeUnknown, Workload: emptyWorkloadEffectEvidence()},
			probeErr: errTestBoundary,
			want:     errTestBoundary,
		},
		{
			name:     eventUnknown,
			probe:    WorkloadEffectProbe{State: WorkloadEffectProbeUnknown, Workload: emptyWorkloadEffectEvidence()},
			probeErr: nil,
			want:     ErrConflictingState,
		},
		{
			name:     testInvalidStateName,
			probe:    WorkloadEffectProbe{State: WorkloadEffectProbeState(99), Workload: emptyWorkloadEffectEvidence()},
			probeErr: nil,
			want:     ErrConflictingState,
		},
		{
			name: testMissingProofName,
			probe: WorkloadEffectProbe{
				State:    WorkloadEffectProbeMissing,
				Workload: exact,
			},
			probeErr: nil,
			want:     ErrConflictingState,
		},
		{
			name: "created",
			probe: WorkloadEffectProbe{
				State:    WorkloadEffectProbeObserved,
				Workload: createdWorkloadEffectEvidence(workload, identifier.String()),
			},
			probeErr: nil,
			want:     ErrConflictingState,
			resolved: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			journal := imageEffectJournal(store.ActionStateIntent)
			runtime := observedWorkloadStartRuntime(&journal.events, workload, identifier.String())
			runtime.probe = test.probe
			runtime.probeErr = test.probeErr

			got, err := runWorkloadStart(context.Background(), journal, identifier, 2, workload, runtime)
			if !errors.Is(err, test.want) || !workloadStartTestResultMatches(got, journal.action, test.resolved) {
				t.Fatalf("runWorkloadStart(%s) = %#v, %v", test.name, got, err)
			}
		})
	}
}

func workloadStartTestResultMatches(
	postcondition EffectPostcondition,
	action store.Action,
	resolved bool,
) bool {
	if resolved {
		return postcondition.Digest != (domain.Digest{}) && !postcondition.Satisfied &&
			action.State == store.ActionStateCompleted
	}

	return postcondition == emptyEffectPostcondition() &&
		action.State == store.ActionStateEffectOutcomeUnknown
}

func TestRunWorkloadStartRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := observedWorkloadStartRuntime(&journal.events, workload, store.TransactionID{1}.String())

	for _, test := range []struct {
		name       string
		identifier store.TransactionID
		sequence   int64
		workload   domain.DesiredWorkload
		runtime    WorkloadStartRuntime
	}{
		{name: "identifier", identifier: store.TransactionID{}, sequence: 2, workload: workload, runtime: runtime},
		{name: testSequenceName, identifier: store.TransactionID{1}, sequence: 0, workload: workload, runtime: runtime},
		{
			name:       "workload",
			identifier: store.TransactionID{1},
			sequence:   2,
			workload:   emptyDesiredWorkload(),
			runtime:    runtime,
		},
		{name: testRuntimeName, identifier: store.TransactionID{1}, sequence: 2, workload: workload, runtime: nil},
	} {
		got, err := runWorkloadStart(
			context.Background(),
			journal,
			test.identifier,
			test.sequence,
			test.workload,
			test.runtime,
		)
		if !errors.Is(err, ErrInvalidRequest) || got != emptyEffectPostcondition() {
			t.Fatalf("runWorkloadStart(%s) = %#v, %v", test.name, got, err)
		}
	}
}

func TestRunWorkloadCreateRecoveryProbesByOwnershipWithoutReplay(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateEffectOutcomeUnknown)
	runtime := observedWorkloadEffectRuntime(&journal.events, workload, identifier.String())

	got, err := runWorkloadCreate(
		context.Background(), journal, identifier, 1, workload, runtime, defaultWorkloadCreateOptions(),
	)
	if err != nil || !got.Satisfied || runtime.createInvoked || runtime.probeResponseID != "" {
		t.Fatalf("runWorkloadCreate(unknown) = %#v, %v", got, err)
	}

	wantEvents := []string{eventIntent, eventProbe, eventComplete}
	if !equalEvents(journal.events, wantEvents) {
		t.Fatalf("events = %q, want %q", journal.events, wantEvents)
	}
}

func TestRunWorkloadCreateUsesTypedProbeAfterLostResponse(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := observedWorkloadEffectRuntime(&journal.events, workload, identifier.String())
	runtime.createErr = errTestBoundary

	got, err := runWorkloadCreate(
		context.Background(), journal, identifier, 1, workload, runtime, defaultWorkloadCreateOptions(),
	)
	if err != nil || !got.Satisfied {
		t.Fatalf("runWorkloadCreate(lost response) = %#v, %v", got, err)
	}
}

func TestRunWorkloadCreateCompletesProvenAbsenceBeforeFailing(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := &testWorkloadEffectRuntime{
		events:    &journal.events,
		createID:  "",
		createErr: errTestBoundary,
		probe: WorkloadEffectProbe{
			State:    WorkloadEffectProbeMissing,
			Workload: emptyWorkloadEffectEvidence(),
		},
		probeErr:        nil,
		created:         emptyDesiredWorkload(),
		transaction:     "",
		probeResponseID: "",
		createInvoked:   false,
	}

	got, err := runWorkloadCreate(
		context.Background(), journal, identifier, 1, workload, runtime, defaultWorkloadCreateOptions(),
	)
	if !errors.Is(err, errTestBoundary) || got.Satisfied || got.Digest == (domain.Digest{}) ||
		journal.action.State != store.ActionStateCompleted {
		t.Fatalf("runWorkloadCreate(missing) = %#v, %v", got, err)
	}

	wantDigest := workloadEffectDigest(
		workloadEffectMissing,
		workloadCreateActionKind,
		workload,
		identifier.String(),
		"",
	)
	if got.Digest != wantDigest {
		t.Fatal("missing workload postcondition digest does not identify proven absence")
	}
}

//nolint:funlen // The table keeps every invalid typed postcondition under one assertion.
func TestRunWorkloadCreateRejectsUnprovenPostconditions(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	identifier := store.TransactionID{1}
	exact := createdWorkloadEffectEvidence(workload, identifier.String())

	type postconditionTest struct {
		name     string
		probe    WorkloadEffectProbe
		probeErr error
		want     error
	}

	mutations := []struct {
		name   string
		mutate func(*WorkloadEffectEvidence)
	}{
		{name: "ID", mutate: func(value *WorkloadEffectEvidence) { value.ID = "" }},
		{name: "name", mutate: func(value *WorkloadEffectEvidence) { value.Name = testOtherValue }},
		{name: "configuration", mutate: func(value *WorkloadEffectEvidence) { value.ConfigurationMatches = false }},
		{name: "lifecycle", mutate: func(value *WorkloadEffectEvidence) { value.Lifecycle = WorkloadLifecycleRunning }},
		{name: "ownership", mutate: func(value *WorkloadEffectEvidence) {
			value.Ownership.Transaction = testOtherValue
		}},
		{name: "response ID", mutate: func(value *WorkloadEffectEvidence) { value.ID = strings.Repeat("b", 64) }},
	}

	tests := make([]postconditionTest, 0, 4+len(mutations))
	tests = append(tests,
		postconditionTest{
			name:     testProbeFailureName,
			probe:    WorkloadEffectProbe{State: WorkloadEffectProbeUnknown, Workload: emptyWorkloadEffectEvidence()},
			probeErr: errTestBoundary,
			want:     errTestBoundary,
		},
		postconditionTest{
			name:     eventUnknown,
			probe:    WorkloadEffectProbe{State: WorkloadEffectProbeUnknown, Workload: emptyWorkloadEffectEvidence()},
			probeErr: nil,
			want:     ErrConflictingState,
		},
		postconditionTest{
			name:     testInvalidStateName,
			probe:    WorkloadEffectProbe{State: WorkloadEffectProbeState(99), Workload: emptyWorkloadEffectEvidence()},
			probeErr: nil,
			want:     ErrConflictingState,
		},
		postconditionTest{
			name: testMissingProofName,
			probe: WorkloadEffectProbe{
				State:    WorkloadEffectProbeMissing,
				Workload: exact,
			},
			probeErr: nil,
			want:     ErrConflictingState,
		},
	)

	for _, mutation := range mutations {
		evidence := exact
		mutation.mutate(&evidence)
		tests = append(tests, postconditionTest{
			name:     mutation.name,
			probe:    WorkloadEffectProbe{State: WorkloadEffectProbeObserved, Workload: evidence},
			probeErr: nil,
			want:     ErrConflictingState,
		})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			journal := imageEffectJournal(store.ActionStateIntent)
			runtime := observedWorkloadEffectRuntime(&journal.events, workload, identifier.String())
			runtime.probe = test.probe
			runtime.probeErr = test.probeErr

			got, err := runWorkloadCreate(
				context.Background(), journal, identifier, 1, workload, runtime, defaultWorkloadCreateOptions(),
			)
			if !errors.Is(err, test.want) || got != emptyEffectPostcondition() ||
				journal.action.State != store.ActionStateEffectOutcomeUnknown {
				t.Fatalf("runWorkloadCreate(%s) = %#v, %v", test.name, got, err)
			}
		})
	}
}

//nolint:funlen // The table keeps every invalid workload identity under one assertion.
func TestRunWorkloadCreateRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	valid := testWorkloadEffect(t)
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := observedWorkloadEffectRuntime(&journal.events, valid, store.TransactionID{1}.String())

	invalidWorkloads := []domain.DesiredWorkload{
		emptyDesiredWorkload(),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) { value.ServiceName = "" }),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) { value.ContainerName = "" }),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) { value.SourceDigest = domain.Digest{} }),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) { value.EffectiveDigest = domain.Digest{} }),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) {
			value.Image.Origin = domain.ImageOriginUnknown
		}),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) { value.Image.Reference = testInvalidValue }),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) {
			value.Image.ReferenceDigest = domain.Hash([]byte("different registry reference"))
		}),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) {
			value.Image.ImageConfig = domain.Digest{}
		}),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) {
			value.Image.Origin = domain.ImageOriginDockerArchive
		}),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) {
			value.Image.Origin = domain.ImageOriginDockerArchive
			value.Image.Reference = testInvalidValue
		}),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) {
			value.Entrypoint = nil
			value.Command = nil
		}),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) {
			value.Entrypoint = []string{string([]byte{0xff})}
		}),
		mutateWorkloadEffect(valid, func(value *domain.DesiredWorkload) { value.Command = []string{"bad\x00value"} }),
	}

	for index, workload := range invalidWorkloads {
		got, err := runWorkloadCreate(
			context.Background(),
			journal,
			store.TransactionID{1},
			1,
			workload,
			runtime,
			defaultWorkloadCreateOptions(),
		)
		if !errors.Is(err, ErrInvalidRequest) || got != emptyEffectPostcondition() {
			t.Fatalf("runWorkloadCreate(invalid workload %d) = %#v, %v", index, got, err)
		}
	}

	for _, test := range []struct {
		name       string
		identifier store.TransactionID
		sequence   int64
		runtime    WorkloadEffectRuntime
	}{
		{name: "identifier", identifier: store.TransactionID{}, sequence: 1, runtime: runtime},
		{name: testSequenceName, identifier: store.TransactionID{1}, sequence: 0, runtime: runtime},
		{name: testRuntimeName, identifier: store.TransactionID{1}, sequence: 1, runtime: nil},
	} {
		got, err := runWorkloadCreate(
			context.Background(),
			journal,
			test.identifier,
			test.sequence,
			valid,
			test.runtime,
			defaultWorkloadCreateOptions(),
		)
		if !errors.Is(err, ErrInvalidRequest) || got != emptyEffectPostcondition() {
			t.Fatalf("runWorkloadCreate(%s) = %#v, %v", test.name, got, err)
		}
	}
}

func TestWorkloadEffectDigestBindsCompleteStableIdentity(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	transaction := store.TransactionID{1}.String()
	intent := workloadEffectDigest(workloadEffectIntent, workloadCreateActionKind, workload, transaction, "")
	emptyCommand := workload
	emptyCommand.Command = []string{}
	nilCommand := workload
	nilCommand.Command = nil
	emptyCommandDigest := workloadEffectDigest(
		workloadEffectIntent,
		workloadCreateActionKind,
		emptyCommand,
		transaction,
		"",
	)

	changed := []domain.Digest{
		workloadObservedEffectDigest(
			workloadEffectObserved,
			workloadCreateActionKind,
			workload,
			transaction,
			testWorkloadEffectID,
			domain.Hash([]byte("runtime storage")),
		),
		workloadEffectDigest(workloadEffectIntent, workloadStartActionKind, workload, transaction, ""),
		workloadEffectDigest(workloadEffectIntent, workloadCreateActionKind, workload, testOtherValue, ""),
		emptyCommandDigest,
	}
	for _, digest := range changed {
		if intent == digest {
			t.Fatal("workload effect digest does not bind its format, state, and complete identity")
		}
	}

	nilCommandDigest := workloadEffectDigest(
		workloadEffectIntent,
		workloadCreateActionKind,
		nilCommand,
		transaction,
		"",
	)
	if intent == (domain.Digest{}) || emptyCommandDigest == nilCommandDigest ||
		intent != workloadEffectDigest(workloadEffectIntent, workloadCreateActionKind, workload, transaction, "") {
		t.Fatal("workload effect digest is empty, unstable, or conflates empty and absent command values")
	}
}

func testWorkloadEffect(t *testing.T) domain.DesiredWorkload {
	t.Helper()

	return domain.DesiredWorkload{
		WorkloadSpec: domain.WorkloadSpec{
			ServiceName:   testServiceName,
			ContainerName: "example-api",
			Platform:      domain.Platform{OS: "linux", Architecture: "amd64"},
			Entrypoint:    []string{testProcessEntrypoint},
			Command:       []string{testProcessCommand},
			NetworkMode:   "bridge",
		},
		Image:           testImageEffectIdentity(t),
		SourceDigest:    domain.Hash([]byte("workload source")),
		EffectiveDigest: domain.Hash([]byte("workload desired state")),
	}
}

func mutateWorkloadEffect(
	workload domain.DesiredWorkload,
	mutate func(*domain.DesiredWorkload),
) domain.DesiredWorkload {
	mutate(&workload)

	return workload
}

func observedWorkloadEffectRuntime(
	events *[]string,
	workload domain.DesiredWorkload,
	transaction string,
) *testWorkloadEffectRuntime {
	return &testWorkloadEffectRuntime{
		events:    events,
		createID:  testWorkloadEffectID,
		createErr: nil,
		probe: WorkloadEffectProbe{
			State:    WorkloadEffectProbeObserved,
			Workload: createdWorkloadEffectEvidence(workload, transaction),
		},
		probeErr:        nil,
		created:         emptyDesiredWorkload(),
		transaction:     "",
		probeResponseID: "",
		createInvoked:   false,
	}
}

func observedWorkloadStartRuntime(
	events *[]string,
	workload domain.DesiredWorkload,
	transaction string,
) *testWorkloadStartRuntime {
	return &testWorkloadStartRuntime{
		events:   events,
		startErr: nil,
		probe: WorkloadEffectProbe{
			State:    WorkloadEffectProbeObserved,
			Workload: startedWorkloadEffectEvidence(workload, transaction),
		},
		probeErr:     nil,
		started:      emptyDesiredWorkload(),
		transaction:  "",
		startInvoked: false,
	}
}

func startedWorkloadEffectEvidence(
	workload domain.DesiredWorkload,
	transaction string,
) WorkloadEffectEvidence {
	evidence := createdWorkloadEffectEvidence(workload, transaction)
	evidence.Lifecycle = WorkloadLifecycleRunning

	return evidence
}

func createdWorkloadEffectEvidence(
	workload domain.DesiredWorkload,
	transaction string,
) WorkloadEffectEvidence {
	runtimeMounts := testRuntimeMounts(workload)
	storageDigest, valid := domain.ComputeStorageDigest(workload, runtimeMounts)
	if !valid {
		panic("invalid test storage evidence")
	}

	return WorkloadEffectEvidence{
		ID:                   testWorkloadEffectID,
		Name:                 workload.ContainerName,
		ConfigurationDigest:  domain.Hash([]byte("runtime configuration")),
		StorageDigest:        storageDigest,
		RuntimeMounts:        runtimeMounts,
		ConfigurationMatches: true,
		Lifecycle:            WorkloadLifecycleCreated,
		Ownership: domain.WorkloadOwnership{
			Status:           domain.OwnershipManaged,
			Service:          workload.ServiceName,
			Transaction:      transaction,
			DesiredState:     workload.EffectiveDigest,
			Reference:        workload.Image.ReferenceDigest,
			ImageConfig:      workload.Image.ImageConfig,
			PlatformManifest: workload.Image.PlatformManifest,
		},
	}
}

func testRuntimeMounts(workload domain.DesiredWorkload) []domain.RuntimeMount {
	if len(workload.Mounts) == 0 {
		return nil
	}

	result := make([]domain.RuntimeMount, len(workload.Mounts))
	for index, mount := range workload.Mounts {
		result[index] = domain.RuntimeMount{
			Kind: mount.Kind, Source: mount.Source, Target: mount.Target, ReadOnly: mount.ReadOnly,
		}
		if mount.Kind == domain.MountVolume {
			result[index].Name = "test-volume"
			result[index].Source = "/runtime/volumes/test-volume"
		}
	}

	return result
}
