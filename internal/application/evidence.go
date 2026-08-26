package application

import (
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	// EvidenceBundleVersion identifies the current transport-neutral evidence
	// projection.
	EvidenceBundleVersion = 1

	maximumEvidenceItems     = 64
	maximumEvidenceNameBytes = 256
	baseEvidenceItems        = 5
	maximumEvidencePerAction = 2
)

// EvidenceKind identifies one bounded application-owned evidence class.
type EvidenceKind string

const (
	// EvidencePlanDesired identifies the desired workload digest.
	EvidencePlanDesired EvidenceKind = "plan_desired"
	// EvidenceRuntimeExecution identifies the probed runtime execution digest.
	EvidenceRuntimeExecution EvidenceKind = "runtime_execution"
	// EvidenceWorkloadObservation identifies capture-time workload evidence.
	EvidenceWorkloadObservation EvidenceKind = "workload_observation"
	// EvidenceTransaction identifies durable transaction state.
	EvidenceTransaction EvidenceKind = "journal_transaction"
	// EvidenceAppliedService identifies the latest durable baseline.
	EvidenceAppliedService EvidenceKind = "applied_service"
	// EvidenceActionIntent identifies one durable action intent.
	EvidenceActionIntent EvidenceKind = "action_intent"
	// EvidenceActionPostcondition identifies one completed action proof.
	EvidenceActionPostcondition EvidenceKind = "action_postcondition"
)

// EvidenceItem contains one stable public identifier, state, and opaque
// identity. Identity never contains the content that produced a digest.
type EvidenceItem struct {
	ID       string       `json:"id"`
	Kind     EvidenceKind `json:"kind"`
	State    string       `json:"state"`
	Identity string       `json:"identity,omitempty"`
}

// EvidenceBundle is a bounded, redacted projection derived from one correlated
// snapshot. Truncated reports omitted action evidence.
type EvidenceBundle struct {
	Version       int            `json:"version"`
	CapturedAt    int64          `json:"captured_at"`
	Project       string         `json:"project"`
	Service       string         `json:"service"`
	DroppedEvents uint64         `json:"dropped_events"`
	Items         []EvidenceItem `json:"items"`
	Truncated     bool           `json:"truncated"`
}

// Evidence derives a bounded bundle without exposing Compose content, image
// configuration, runtime object IDs, private paths, or environment values.
func (facade *ApplyFacade) Evidence(snapshot OperationSnapshot) (EvidenceBundle, error) {
	var empty EvidenceBundle
	if facade == nil || !validEvidenceSnapshot(snapshot) {
		return empty, ErrInvalidRequest
	}

	bundle := EvidenceBundle{
		Version:       EvidenceBundleVersion,
		CapturedAt:    snapshot.CapturedAt.Unix(),
		Project:       snapshot.Plan.Project,
		Service:       snapshot.Plan.Service,
		DroppedEvents: snapshot.DroppedEvents,
		Items: make(
			[]EvidenceItem,
			0,
			min(maximumEvidenceItems, baseEvidenceItems+len(snapshot.Actions)*maximumEvidencePerAction),
		),
		Truncated: false,
	}
	appendEvidence(&bundle, EvidenceItem{
		ID: "plan.desired", Kind: EvidencePlanDesired,
		State: string(snapshot.Plan.Kind), Identity: snapshot.Plan.Desired.String(),
	})
	appendEvidence(&bundle, EvidenceItem{
		ID: "runtime.execution", Kind: EvidenceRuntimeExecution,
		State: snapshot.Runtime.Kind.String(), Identity: snapshot.Runtime.Digest.String(),
	})
	appendEvidence(&bundle, workloadEvidence(snapshot.Plan.Observation))
	appendEvidence(&bundle, transactionEvidence(snapshot))
	appendEvidence(&bundle, appliedServiceEvidence(snapshot))

	for _, action := range snapshot.Actions {
		appendEvidence(&bundle, EvidenceItem{
			ID: fmt.Sprintf("action.%d.intent", action.Sequence), Kind: EvidenceActionIntent,
			State: action.State, Identity: action.Intent,
		})
		if action.Postcondition != "" {
			appendEvidence(&bundle, EvidenceItem{
				ID:   fmt.Sprintf("action.%d.postcondition", action.Sequence),
				Kind: EvidenceActionPostcondition, State: action.State, Identity: action.Postcondition,
			})
		}
	}

	return bundle, nil
}

func appendEvidence(bundle *EvidenceBundle, item EvidenceItem) {
	if len(bundle.Items) == maximumEvidenceItems {
		bundle.Truncated = true

		return
	}

	bundle.Items = append(bundle.Items, item)
}

func workloadEvidence(observation WorkloadObservation) EvidenceItem {
	item := EvidenceItem{ID: "workload.observation", Kind: EvidenceWorkloadObservation}
	if observation.State == WorkloadObservationMissing {
		item.State = "missing"

		return item
	}

	item.State = "present"
	item.Identity = observation.ConfigurationDigest.String()

	return item
}

func transactionEvidence(snapshot OperationSnapshot) EvidenceItem {
	item := EvidenceItem{ID: "journal.transaction", Kind: EvidenceTransaction, State: "unavailable"}
	if snapshot.HasTransaction {
		item.State = snapshot.Transaction.State
		item.Identity = snapshot.Transaction.ID
	}

	return item
}

func appliedServiceEvidence(snapshot OperationSnapshot) EvidenceItem {
	item := EvidenceItem{ID: "journal.applied", Kind: EvidenceAppliedService, State: "unavailable"}
	if snapshot.HasApplied {
		item.State = "present"
		item.Identity = snapshot.Applied.Transaction
	}

	return item
}

func validEvidenceSnapshot(snapshot OperationSnapshot) bool {
	if !validEvidenceSnapshotCore(snapshot) || !validEvidenceObservation(snapshot.Plan.Observation) {
		return false
	}

	return validSnapshotTransaction(snapshot) && validSnapshotApplied(snapshot) &&
		validSnapshotActions(snapshot)
}

func validEvidenceSnapshotCore(snapshot OperationSnapshot) bool {
	return !snapshot.CapturedAt.IsZero() && validEvidenceName(snapshot.Plan.Project) &&
		validEvidenceName(snapshot.Plan.Service) && validPlanKind(snapshot.Plan.Kind) &&
		validRuntimeEvidence(snapshot.Runtime) && snapshot.Runtime.Digest != (domain.Digest{}) &&
		snapshot.Plan.Runtime == snapshot.Runtime.Kind && snapshot.Plan.Desired != (domain.Digest{})
}

func validEvidenceObservation(observation WorkloadObservation) bool {
	switch observation.State {
	case WorkloadObservationUnknown:
		return false
	case WorkloadObservationMissing:
		return validMissingWorkloadObservation(observation)
	case WorkloadObservationPresent:
		return validPresentWorkloadObservation(observation)
	default:
		return false
	}
}

func validEvidenceName(value string) bool {
	return value != "" && len(value) <= maximumEvidenceNameBytes
}

func validPlanKind(kind PlanKind) bool {
	switch kind {
	case PlanBootstrap, PlanAdopt, PlanUnchanged, PlanUpgrade, PlanResume,
		PlanProbeUnknownEffect, PlanRestore:
		return true
	default:
		return false
	}
}

func validSnapshotTransaction(snapshot OperationSnapshot) bool {
	if !snapshot.HasTransaction {
		return snapshot.Transaction == (SnapshotTransaction{}) && len(snapshot.Actions) == 0
	}

	transaction := snapshot.Transaction
	if !validSnapshotTransactionIdentity(transaction) || !validSnapshotTransactionKind(transaction) {
		return false
	}

	switch store.TransactionState(transaction.State) {
	case store.TransactionActive, store.TransactionDegraded:
		return true
	case store.TransactionFailed, store.TransactionSucceeded:
		return false
	default:
		return false
	}
}

func validSnapshotTransactionIdentity(transaction SnapshotTransaction) bool {
	return validTransactionID(transaction.ID) && transaction.Runtime.SupportsWorkloads() &&
		validDigestIdentity(transaction.Source) && validDigestIdentity(transaction.Desired) &&
		validDigestIdentity(transaction.Execution)
}

func validSnapshotTransactionKind(transaction SnapshotTransaction) bool {
	switch store.TransactionKind(transaction.Kind) {
	case store.TransactionBootstrap, store.TransactionAdopt:
		return transaction.BaseTransaction == ""
	case store.TransactionUpgrade:
		return validTransactionID(transaction.BaseTransaction)
	default:
		return false
	}
}

func validSnapshotApplied(snapshot OperationSnapshot) bool {
	if !snapshot.HasApplied {
		return snapshot.Applied == (SnapshotAppliedService{})
	}

	applied := snapshot.Applied

	return validTransactionID(applied.Transaction) && applied.Runtime.SupportsWorkloads() &&
		validSnapshotAppliedDigests(applied)
}

func validSnapshotAppliedDigests(applied SnapshotAppliedService) bool {
	return validDigestIdentity(applied.Source) && validDigestIdentity(applied.Desired) &&
		validDigestIdentity(applied.Execution) && validDigestIdentity(applied.Configuration) &&
		validDigestIdentity(applied.Storage) && validDigestIdentity(applied.Reference) &&
		validDigestIdentity(applied.Manifest) && validDigestIdentity(applied.ImageConfig)
}

func validSnapshotActions(snapshot OperationSnapshot) bool {
	for _, action := range snapshot.Actions {
		if action.Sequence <= 0 || action.Kind == "" || !validDigestIdentity(action.Intent) {
			return false
		}

		switch store.ActionState(action.State) {
		case store.ActionStateIntent, store.ActionStateEffectOutcomeUnknown:
			if action.Postcondition != "" {
				return false
			}
		case store.ActionStateCompleted:
			if !validDigestIdentity(action.Postcondition) {
				return false
			}
		default:
			return false
		}
	}

	return true
}

func validTransactionID(value string) bool {
	return len(value) == len(store.TransactionID{})*2 && value != (store.TransactionID{}).String() &&
		validLowerHex(value)
}

func validLowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}

	return true
}

func validDigestIdentity(value string) bool {
	digest, err := domain.ParseDigest(value)

	return err == nil && digest != (domain.Digest{})
}
