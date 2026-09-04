package store

import (
	"encoding/hex"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const transactionIDBytes = 16

// TransactionKind identifies the immutable apply journey owned by a journal.
type TransactionKind string

const (
	// TransactionBootstrap creates the first transaction-owned workload.
	TransactionBootstrap TransactionKind = "bootstrap"
	// TransactionAdopt records an existing unmanaged workload without changing it.
	TransactionAdopt TransactionKind = "adopt"
	// TransactionUpgrade replaces the currently applied workload.
	TransactionUpgrade TransactionKind = "upgrade"
)

// TransactionID identifies one apply transaction in runtime ownership labels
// and durable state.
type TransactionID [transactionIDBytes]byte

// String returns the lowercase hexadecimal transaction identifier.
func (identifier TransactionID) String() string {
	return hex.EncodeToString(identifier[:])
}

// TransactionState describes whether a durable transaction still requires
// automatic recovery.
type TransactionState string

const (
	// TransactionActive permits the existing transaction to continue.
	TransactionActive TransactionState = "active"
	// TransactionDegraded requires recovery of the previously applied workload.
	TransactionDegraded TransactionState = "degraded"
	// TransactionHealthDegraded retains the current workload until an explicit
	// operator rollback or later healthy convergence.
	TransactionHealthDegraded TransactionState = "health_degraded"
	// TransactionFailed records a terminal unsuccessful transaction.
	TransactionFailed TransactionState = "failed"
	// TransactionSucceeded records a terminal successful transaction.
	TransactionSucceeded TransactionState = "succeeded"
)

// TransactionIntent binds one transaction to its desired and runtime execution
// identities without persisting recoverable workload configuration.
type TransactionIntent struct {
	Kind                     TransactionKind
	Runtime                  domain.RuntimeKind
	SourceDigest             domain.Digest
	EffectiveDigest          domain.Digest
	ExecutionDigest          domain.Digest
	RepositoryVersion        int
	RepositoryScopeDigest    domain.Digest
	RepositoryLocationDigest domain.Digest
	HasRepository            bool
	BaseTransactionID        TransactionID
	HasBaseTransaction       bool
	PredecessorWorkloadID    string
}

// Transaction is one immutable transaction identity and its current state.
type Transaction struct {
	ID                       TransactionID
	Kind                     TransactionKind
	State                    TransactionState
	Runtime                  domain.RuntimeKind
	SourceDigest             domain.Digest
	EffectiveDigest          domain.Digest
	ExecutionDigest          domain.Digest
	RepositoryVersion        int
	RepositoryScopeDigest    domain.Digest
	RepositoryLocationDigest domain.Digest
	HasRepository            bool
	BaseTransactionID        TransactionID
	HasBaseTransaction       bool
	PredecessorWorkloadID    string
}

// AppliedServiceIntent is the complete opaque baseline published when a
// transaction succeeds. It does not retain reconstructible workload config.
type AppliedServiceIntent struct {
	WorkloadID             string
	ConfigurationDigest    domain.Digest
	StorageDigest          domain.Digest
	ReferenceDigest        domain.Digest
	PlatformManifestDigest domain.Digest
	ImageConfigDigest      domain.Digest
	Healthcheck            bool
	Backup                 *BackupIndexIntent
}

// AppliedService is the latest committed workload generation for one service.
type AppliedService struct {
	TransactionID          TransactionID
	Kind                   TransactionKind
	Runtime                domain.RuntimeKind
	SourceDigest           domain.Digest
	EffectiveDigest        domain.Digest
	ExecutionDigest        domain.Digest
	WorkloadID             string
	ConfigurationDigest    domain.Digest
	StorageDigest          domain.Digest
	ReferenceDigest        domain.Digest
	PlatformManifestDigest domain.Digest
	ImageConfigDigest      domain.Digest
	Healthcheck            bool
}

// BackupIndexIntent binds a complete manifest to the successful upgrade that
// produced it. ManifestPath is relative to the private backup root.
type BackupIndexIntent struct {
	ManifestPath   string
	ManifestDigest domain.Digest
	CreatedUnix    int64
}

// BackupIndex locates one complete transaction-owned workload backup.
type BackupIndex struct {
	TransactionID  TransactionID
	Runtime        domain.RuntimeKind
	ManifestPath   string
	ManifestDigest domain.Digest
	CreatedUnix    int64
}

// BackupIndexCandidate is one complete manifest identity proposed by a
// maintenance scan. Rebuilding the index accepts it only when every field
// matches an existing successful upgrade transaction.
type BackupIndexCandidate struct {
	TransactionID         TransactionID
	Project               string
	Service               string
	Runtime               domain.RuntimeKind
	SourceDigest          domain.Digest
	EffectiveDigest       domain.Digest
	ExecutionDigest       domain.Digest
	BaseTransactionID     TransactionID
	PredecessorWorkloadID string
	ManifestPath          string
	ManifestDigest        domain.Digest
	CreatedUnix           int64
}

// ActionState describes the durable boundary around one external effect.
type ActionState string

const (
	// ActionStateIntent records an immutable plan before an external effect can start.
	ActionStateIntent ActionState = "intent"
	// ActionStateEffectOutcomeUnknown requires a typed probe before recovery may
	// complete the action or replay its idempotent effect.
	ActionStateEffectOutcomeUnknown ActionState = "effect_outcome_unknown"
	// ActionStateCompleted records a typed postcondition that resolved the effect.
	ActionStateCompleted ActionState = "completed"
)

// ActionIntent identifies the next external effect without retaining its
// private typed payload.
type ActionIntent struct {
	Sequence     int64
	Kind         string
	IntentDigest domain.Digest
}

// Action is one durable external-effect boundary.
type Action struct {
	TransactionID       TransactionID
	Sequence            int64
	Kind                string
	State               ActionState
	IntentDigest        domain.Digest
	PostconditionDigest *domain.Digest
}
