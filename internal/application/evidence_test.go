package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type evidenceCapability interface {
	Evidence(snapshot OperationSnapshot) (EvidenceBundle, error)
}

var _ evidenceCapability = (*ApplyFacade)(nil)

//nolint:cyclop // One projection test checks the complete bounded, redacted consumer contract.
func TestEvidenceProjectsOnlyBoundedOpaqueIdentities(t *testing.T) {
	t.Parallel()

	snapshot := validEvidenceTestSnapshot()
	const secret = "registry-token-secret"
	snapshot.Plan.Image.Reference = "https://user:" + secret + "@registry.example/image"
	snapshot.Plan.Image.Environment = []string{"TOKEN=" + secret}
	snapshot.Plan.Observation.ID = secret
	snapshot.DroppedEvents = 4
	facade := &ApplyFacade{}

	bundle, err := facade.Evidence(snapshot)
	if err != nil {
		t.Fatalf("Evidence() error = %v", err)
	}
	wantKinds := []EvidenceKind{
		EvidencePlanDesired,
		EvidenceRuntimeExecution,
		EvidenceWorkloadObservation,
		EvidenceTransaction,
		EvidenceAppliedService,
		EvidenceActionIntent,
		EvidenceActionPostcondition,
	}
	gotKinds := make([]EvidenceKind, len(bundle.Items))
	for index := range bundle.Items {
		gotKinds[index] = bundle.Items[index].Kind
	}
	if bundle.Version != EvidenceBundleVersion || bundle.CapturedAt != snapshot.CapturedAt.Unix() ||
		bundle.Project != snapshot.Plan.Project || bundle.Service != snapshot.Plan.Service ||
		bundle.DroppedEvents != snapshot.DroppedEvents || bundle.Truncated || !slices.Equal(gotKinds, wantKinds) {
		t.Fatalf("Evidence() = %#v", bundle)
	}

	encoded, encodeErr := json.Marshal(bundle)
	if encodeErr != nil {
		t.Fatalf("encode evidence: %v", encodeErr)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "https://") ||
		strings.Contains(string(encoded), "TOKEN=") {
		t.Fatalf("evidence contains unprojected content: %s", encoded)
	}
}

func TestEvidenceBoundsActionProjection(t *testing.T) {
	t.Parallel()

	snapshot := validEvidenceTestSnapshot()
	snapshot.Actions = make([]SnapshotAction, maximumEvidenceItems)
	for index := range snapshot.Actions {
		snapshot.Actions[index] = SnapshotAction{
			Sequence: int64(index + 1), Kind: workloadCreateActionKind, State: string(store.ActionStateCompleted),
			Intent:        domain.Hash([]byte(fmt.Sprintf("intent-%d", index))).String(),
			Postcondition: domain.Hash([]byte(fmt.Sprintf("postcondition-%d", index))).String(),
		}
	}

	bundle, err := (&ApplyFacade{}).Evidence(snapshot)
	if err != nil || len(bundle.Items) != maximumEvidenceItems || !bundle.Truncated {
		t.Fatalf("Evidence(bounded) = %d items, truncated %t, %v", len(bundle.Items), bundle.Truncated, err)
	}
}

func TestEvidenceRepresentsUnavailableAndMissingEvidence(t *testing.T) {
	t.Parallel()

	snapshot := validEvidenceTestSnapshot()
	snapshot.Plan.Observation = missingObservation()
	snapshot.Transaction = SnapshotTransaction{}
	snapshot.HasTransaction = false
	snapshot.Applied = SnapshotAppliedService{}
	snapshot.HasApplied = false
	snapshot.Actions = nil

	bundle, err := (&ApplyFacade{}).Evidence(snapshot)
	if err != nil {
		t.Fatalf("Evidence(unavailable) error = %v", err)
	}
	if bundle.Items[2].State != "missing" || bundle.Items[2].Identity != "" ||
		bundle.Items[3].State != "unavailable" || bundle.Items[3].Identity != "" ||
		bundle.Items[4].State != "unavailable" || bundle.Items[4].Identity != "" {
		t.Fatalf("unavailable evidence = %#v", bundle.Items[2:5])
	}
}

func TestEvidenceAcceptsEveryStablePlanAndJournalState(t *testing.T) {
	t.Parallel()

	for _, kind := range []PlanKind{
		PlanBootstrap, PlanAdopt, PlanUnchanged, PlanUpgrade, PlanResume, PlanProbeUnknownEffect, PlanRestore,
	} {
		snapshot := validEvidenceTestSnapshot()
		snapshot.Plan.Kind = kind
		if _, err := (&ApplyFacade{}).Evidence(snapshot); err != nil {
			t.Fatalf("Evidence(plan %q) error = %v", kind, err)
		}
	}

	for _, kind := range []store.TransactionKind{
		store.TransactionBootstrap, store.TransactionAdopt, store.TransactionUpgrade,
	} {
		for _, state := range []store.TransactionState{store.TransactionActive, store.TransactionDegraded} {
			snapshot := validEvidenceTestSnapshot()
			snapshot.Transaction.Kind = string(kind)
			snapshot.Transaction.State = string(state)
			if kind == store.TransactionUpgrade {
				snapshot.Transaction.BaseTransaction = store.TransactionID{2}.String()
			} else {
				snapshot.Transaction.BaseTransaction = ""
			}
			if _, err := (&ApplyFacade{}).Evidence(snapshot); err != nil {
				t.Fatalf("Evidence(transaction %q/%q) error = %v", kind, state, err)
			}
		}
	}

	for _, state := range []store.ActionState{
		store.ActionStateIntent, store.ActionStateEffectOutcomeUnknown, store.ActionStateCompleted,
	} {
		snapshot := validEvidenceTestSnapshot()
		snapshot.Actions[0].State = string(state)
		if state == store.ActionStateCompleted {
			snapshot.Actions[0].Postcondition = domain.Hash([]byte("postcondition")).String()
		} else {
			snapshot.Actions[0].Postcondition = ""
		}
		if _, err := (&ApplyFacade{}).Evidence(snapshot); err != nil {
			t.Fatalf("Evidence(action %q) error = %v", state, err)
		}
	}
}

//nolint:funlen // The mutation table audits every externally constructible projection boundary.
func TestEvidenceRejectsInvalidSnapshotProjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*OperationSnapshot)
	}{
		{name: "capture time", mutate: func(value *OperationSnapshot) { value.CapturedAt = time.Time{} }},
		{name: "project empty", mutate: func(value *OperationSnapshot) { value.Plan.Project = "" }},
		{name: "project large", mutate: func(value *OperationSnapshot) {
			value.Plan.Project = strings.Repeat("a", maximumEvidenceNameBytes+1)
		}},
		{name: "service empty", mutate: func(value *OperationSnapshot) { value.Plan.Service = "" }},
		{name: "service large", mutate: func(value *OperationSnapshot) {
			value.Plan.Service = strings.Repeat("a", maximumEvidenceNameBytes+1)
		}},
		{name: "plan kind", mutate: func(value *OperationSnapshot) { value.Plan.Kind = PlanKind(eventUnknown) }},
		{name: "runtime kind", mutate: func(value *OperationSnapshot) {
			value.Runtime.Kind = domain.RuntimeKind(eventUnknown)
		}},
		{name: "runtime platform OS", mutate: func(value *OperationSnapshot) { value.Runtime.Platform.OS = "" }},
		{name: "runtime platform architecture", mutate: func(value *OperationSnapshot) {
			value.Runtime.Platform.Architecture = ""
		}},
		{name: "runtime identity", mutate: func(value *OperationSnapshot) { value.Runtime.Digest = domain.Digest{} }},
		{name: "runtime mismatch", mutate: func(value *OperationSnapshot) { value.Plan.Runtime = domain.RuntimePodman }},
		{name: "desired identity", mutate: func(value *OperationSnapshot) { value.Plan.Desired = domain.Digest{} }},
		{name: "observation state", mutate: func(value *OperationSnapshot) {
			value.Plan.Observation.State = WorkloadObservationUnknown
		}},
		{name: "invalid observation state", mutate: func(value *OperationSnapshot) {
			value.Plan.Observation.State = WorkloadObservationState(255)
		}},
		{name: "missing observation content", mutate: func(value *OperationSnapshot) {
			value.Plan.Observation = missingObservation()
			value.Plan.Observation.ID = "unexpected"
		}},
		{name: "present observation identity", mutate: func(value *OperationSnapshot) {
			value.Plan.Observation.ConfigurationDigest = domain.Digest{}
		}},
		{name: "absent transaction content", mutate: func(value *OperationSnapshot) { value.HasTransaction = false }},
		{name: "transaction ID", mutate: func(value *OperationSnapshot) { value.Transaction.ID = testInvalidValue }},
		{name: "zero transaction ID", mutate: func(value *OperationSnapshot) {
			value.Transaction.ID = (store.TransactionID{}).String()
		}},
		{name: "transaction ID uppercase", mutate: func(value *OperationSnapshot) {
			value.Transaction.ID = "A" + value.Transaction.ID[1:]
		}},
		{name: "transaction runtime", mutate: func(value *OperationSnapshot) {
			value.Transaction.Runtime = domain.RuntimeKind(eventUnknown)
		}},
		{name: "transaction source", mutate: func(value *OperationSnapshot) {
			value.Transaction.Source = testInvalidValue
		}},
		{name: "zero transaction source", mutate: func(value *OperationSnapshot) {
			value.Transaction.Source = (domain.Digest{}).String()
		}},
		{name: "transaction desired", mutate: func(value *OperationSnapshot) {
			value.Transaction.Desired = testInvalidValue
		}},
		{name: "transaction execution", mutate: func(value *OperationSnapshot) {
			value.Transaction.Execution = testInvalidValue
		}},
		{name: "bootstrap base", mutate: func(value *OperationSnapshot) {
			value.Transaction.BaseTransaction = store.TransactionID{2}.String()
		}},
		{name: "upgrade base", mutate: func(value *OperationSnapshot) {
			value.Transaction.Kind = string(store.TransactionUpgrade)
			value.Transaction.BaseTransaction = testInvalidValue
		}},
		{name: "transaction kind", mutate: func(value *OperationSnapshot) { value.Transaction.Kind = eventUnknown }},
		{name: "failed transaction state", mutate: func(value *OperationSnapshot) {
			value.Transaction.State = string(store.TransactionFailed)
		}},
		{name: "succeeded transaction state", mutate: func(value *OperationSnapshot) {
			value.Transaction.State = string(store.TransactionSucceeded)
		}},
		{name: "invalid transaction state", mutate: func(value *OperationSnapshot) {
			value.Transaction.State = testInvalidValue
		}},
		{name: "absent applied content", mutate: func(value *OperationSnapshot) { value.HasApplied = false }},
		{name: "applied transaction", mutate: func(value *OperationSnapshot) {
			value.Applied.Transaction = testInvalidValue
		}},
		{name: "applied runtime", mutate: func(value *OperationSnapshot) {
			value.Applied.Runtime = domain.RuntimeKind(eventUnknown)
		}},
		{name: "applied source", mutate: func(value *OperationSnapshot) { value.Applied.Source = testInvalidValue }},
		{name: "applied desired", mutate: func(value *OperationSnapshot) { value.Applied.Desired = testInvalidValue }},
		{name: "applied execution", mutate: func(value *OperationSnapshot) {
			value.Applied.Execution = testInvalidValue
		}},
		{name: "applied configuration", mutate: func(value *OperationSnapshot) {
			value.Applied.Configuration = testInvalidValue
		}},
		{name: "applied storage", mutate: func(value *OperationSnapshot) { value.Applied.Storage = testInvalidValue }},
		{name: "applied reference", mutate: func(value *OperationSnapshot) {
			value.Applied.Reference = testInvalidValue
		}},
		{name: "applied manifest", mutate: func(value *OperationSnapshot) {
			value.Applied.Manifest = testInvalidValue
		}},
		{name: "applied image config", mutate: func(value *OperationSnapshot) {
			value.Applied.ImageConfig = testInvalidValue
		}},
		{name: "action sequence", mutate: func(value *OperationSnapshot) { value.Actions[0].Sequence = 0 }},
		{name: "action kind", mutate: func(value *OperationSnapshot) { value.Actions[0].Kind = "" }},
		{name: "action intent", mutate: func(value *OperationSnapshot) { value.Actions[0].Intent = testInvalidValue }},
		{name: "pending postcondition", mutate: func(value *OperationSnapshot) {
			value.Actions[0].State = string(store.ActionStateIntent)
		}},
		{name: "completed postcondition", mutate: func(value *OperationSnapshot) {
			value.Actions[0].Postcondition = testInvalidValue
		}},
		{name: "action state", mutate: func(value *OperationSnapshot) { value.Actions[0].State = eventUnknown }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			snapshot := validEvidenceTestSnapshot()
			test.mutate(&snapshot)
			if bundle, err := (&ApplyFacade{}).Evidence(snapshot); !emptyEvidenceBundle(bundle) ||
				!errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Evidence(invalid) = %#v, %v", bundle, err)
			}
		})
	}

	bundle, err := (*ApplyFacade)(nil).Evidence(validEvidenceTestSnapshot())
	if !emptyEvidenceBundle(bundle) || !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Evidence(nil facade) = %#v, %v", bundle, err)
	}
}

func emptyEvidenceBundle(bundle EvidenceBundle) bool {
	return bundle.Version == 0 && bundle.CapturedAt == 0 && bundle.Project == "" && bundle.Service == "" &&
		bundle.DroppedEvents == 0 && bundle.Items == nil && !bundle.Truncated
}

func validEvidenceTestSnapshot() OperationSnapshot {
	execution := testExecutionEvidence()
	digest := func(value string) string { return domain.Hash([]byte(value)).String() }
	transaction := store.TransactionID{1}.String()

	return OperationSnapshot{
		CapturedAt: time.Unix(1, 0).UTC(),
		Plan: Plan{
			Kind: PlanResume, Project: testProjectName, Service: testServiceName,
			Runtime: domain.RuntimeDocker, Desired: domain.Hash([]byte("desired")),
			Observation: WorkloadObservation{
				ID: "runtime-object", State: WorkloadObservationPresent,
				ConfigurationDigest: domain.Hash([]byte("configuration")),
				StorageDigest:       domain.Hash([]byte("storage")),
				Ownership:           testOwnership(domain.OwnershipUnmanaged),
			},
		},
		Runtime: execution,
		Transaction: SnapshotTransaction{
			ID: transaction, Kind: string(store.TransactionBootstrap), State: string(store.TransactionActive),
			Runtime: domain.RuntimeDocker, Source: digest("source"), Desired: digest("desired"),
			Execution: execution.Digest.String(),
		},
		HasTransaction: true,
		Applied: SnapshotAppliedService{
			Transaction: transaction, Runtime: domain.RuntimeDocker,
			Source: digest("source"), Desired: digest("desired"), Execution: execution.Digest.String(),
			Configuration: digest("configuration"), Storage: digest("storage"), Reference: digest("reference"),
			Manifest: digest("manifest"), ImageConfig: digest("image-config"),
		},
		HasApplied: true,
		Actions: []SnapshotAction{{
			Sequence: 1, Kind: workloadCreateActionKind, State: string(store.ActionStateCompleted),
			Intent: digest("intent"), Postcondition: digest("postcondition"),
		}},
	}
}
