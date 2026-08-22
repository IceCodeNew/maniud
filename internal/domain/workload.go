package domain

// DesiredWorkload is the runtime-neutral result of projecting one Compose service.
type DesiredWorkload struct {
	WorkloadSpec

	Image           ImageIdentity
	SourceDigest    Digest
	EffectiveDigest Digest
}
