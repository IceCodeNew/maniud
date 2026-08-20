package domain

import "slices"

// ImageOrigin identifies how maniud proves a desired image.
type ImageOrigin uint8

const (
	// ImageOriginUnknown is the fail-closed zero value.
	ImageOriginUnknown ImageOrigin = iota
	// ImageOriginRegistry resolves and pulls immutable registry content.
	ImageOriginRegistry
	// ImageOriginDockerArchive requires an operator-imported local image.
	ImageOriginDockerArchive
)

// Platform identifies one operating-system and architecture image target.
type Platform struct {
	OS           string
	Architecture string
	Variant      string
}

// ImageIdentity records the resolved registry and platform-specific identities.
type ImageIdentity struct {
	Origin           ImageOrigin
	Reference        string
	ReferenceDigest  Digest
	Platform         Platform
	PlatformManifest Digest
	ImageConfig      Digest
	User             string
	Environment      []string
	Entrypoint       []string
	Command          []string
	ExposedPorts     []ExposedPort
	Volumes          []string
	WorkingDirectory string
	Labels           []string
	StopSignal       string
	Healthcheck      *Healthcheck
}

// Clone returns a deep copy of the identity and its decoded configuration.
func (image ImageIdentity) Clone() ImageIdentity {
	clone := image
	clone.Environment = slices.Clone(image.Environment)
	clone.Entrypoint = slices.Clone(image.Entrypoint)
	clone.Command = slices.Clone(image.Command)
	clone.ExposedPorts = slices.Clone(image.ExposedPorts)
	clone.Volumes = slices.Clone(image.Volumes)
	clone.Labels = slices.Clone(image.Labels)
	clone.Healthcheck = cloneHealthcheck(image.Healthcheck)

	return clone
}
