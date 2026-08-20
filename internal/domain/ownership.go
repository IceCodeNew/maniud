package domain

const (
	// LabelService binds a runtime object to one Compose service.
	LabelService = "io.maniud.service"
	// LabelTransaction binds a runtime object to one durable transaction.
	LabelTransaction = "io.maniud.transaction"
	// LabelDesiredStateDigest binds a runtime object to its effective desired state.
	LabelDesiredStateDigest = "io.maniud.desired-state-digest"
	// LabelReferenceDigest records the immutable source image reference digest.
	LabelReferenceDigest = "io.maniud.reference-digest"
	// LabelImageConfigDigest records the runtime image configuration identity.
	LabelImageConfigDigest = "io.maniud.image-config-digest"
	// LabelPlatformManifestDigest records the selected platform manifest identity.
	LabelPlatformManifestDigest = "io.maniud.platform-manifest-digest"
)

// IsOwnershipLabel reports whether a key is reserved for maniud evidence.
func IsOwnershipLabel(value string) bool {
	switch value {
	case LabelService, LabelTransaction, LabelDesiredStateDigest, LabelReferenceDigest,
		LabelImageConfigDigest, LabelPlatformManifestDigest:
		return true
	default:
		return false
	}
}

// OwnershipStatus classifies the complete maniud ownership label set.
type OwnershipStatus uint8

const (
	// OwnershipConflicting is the fail-closed zero value for incomplete or invalid labels.
	OwnershipConflicting OwnershipStatus = iota
	// OwnershipUnmanaged identifies an object without labels in the maniud namespace.
	OwnershipUnmanaged
	// OwnershipManaged identifies a complete, internally consistent label set.
	OwnershipManaged
)

// WorkloadOwnership is runtime-neutral ownership evidence from immutable container labels.
type WorkloadOwnership struct {
	Status           OwnershipStatus
	Service          string
	Transaction      string
	DesiredState     Digest
	Reference        Digest
	ImageConfig      Digest
	PlatformManifest Digest
}

// Matches reports whether ownership is the exact expected transaction and desired state.
func (ownership WorkloadOwnership) Matches(
	service string,
	transaction string,
	desiredState Digest,
	reference Digest,
) bool {
	return ownership.Status == OwnershipManaged && ownership.Service == service &&
		ownership.Transaction == transaction && ownership.DesiredState == desiredState &&
		ownership.Reference == reference
}
