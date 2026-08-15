package store

import (
	"encoding/hex"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const transactionIDBytes = 16

// TransactionID identifies one upgrade transaction in runtime ownership labels
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
	// TransactionFailed records a terminal unsuccessful transaction.
	TransactionFailed TransactionState = "failed"
	// TransactionSucceeded records a terminal successful transaction.
	TransactionSucceeded TransactionState = "succeeded"
)

// TransactionIntent binds one transaction to its desired and runtime execution
// identities without persisting recoverable workload configuration.
type TransactionIntent struct {
	Runtime         domain.RuntimeKind
	SourceDigest    domain.Digest
	EffectiveDigest domain.Digest
	ExecutionDigest domain.Digest
}

// Transaction is one immutable transaction identity and its current state.
type Transaction struct {
	ID              TransactionID
	State           TransactionState
	Runtime         domain.RuntimeKind
	SourceDigest    domain.Digest
	EffectiveDigest domain.Digest
	ExecutionDigest domain.Digest
}

// ActionState describes the durable boundary around one external effect.
type ActionState string

const (
	// ActionStateIntent records an immutable plan before an external effect can start.
	ActionStateIntent ActionState = "intent"
	// ActionStateEffectOutcomeUnknown records that only a typed probe may run next.
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
