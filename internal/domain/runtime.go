package domain

// RuntimeKind identifies a container control-plane protocol.
type RuntimeKind string

const (
	// RuntimeDocker uses the Docker Engine HTTP API.
	RuntimeDocker RuntimeKind = "docker"
	// RuntimePodman uses the Podman Libpod REST API.
	RuntimePodman RuntimeKind = "podman"
	// RuntimeContainerd uses the native containerd gRPC API.
	RuntimeContainerd RuntimeKind = "containerd"
)

// ParseRuntimeKind accepts only the runtime protocols supported in phase one.
func ParseRuntimeKind(value string) (RuntimeKind, bool) {
	switch RuntimeKind(value) {
	case RuntimeDocker:
		return RuntimeDocker, true
	case RuntimePodman:
		return RuntimePodman, true
	case RuntimeContainerd:
		return RuntimeContainerd, true
	default:
		return "", false
	}
}

// SupportsWorkloads reports whether the runtime can mutate workloads.
func (kind RuntimeKind) SupportsWorkloads() bool {
	return kind == RuntimeDocker || kind == RuntimePodman || kind == RuntimeContainerd
}

// String returns the stable wire-independent name.
func (kind RuntimeKind) String() string {
	return string(kind)
}
