package domain

// DesiredWorkload is the runtime-neutral result of projecting one Compose service.
type DesiredWorkload struct {
	ServiceName     string
	ContainerName   string
	Image           ImageIdentity
	Entrypoint      []string
	Command         []string
	SourceDigest    Digest
	EffectiveDigest Digest
}
