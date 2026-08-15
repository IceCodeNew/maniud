package domain

// Platform identifies one operating-system and architecture image target.
type Platform struct {
	OS           string
	Architecture string
	Variant      string
}

// ImageIdentity records the resolved registry and platform-specific identities.
type ImageIdentity struct {
	Reference        string
	ReferenceDigest  Digest
	Platform         Platform
	PlatformManifest Digest
	ImageConfig      Digest
}
