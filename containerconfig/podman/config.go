// Package podman translates portable container configuration to and from the
// native Libpod create and inspect JSON contracts.
package podman

import (
	"maps"
	"slices"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	defaultSharedMemoryBytes = int64(65_536_000)
	defaultStopTimeout       = int64(10)
	containerIDBytes         = 64
	containerNameBytes       = 63
	serviceNameBytes         = 128
	hostAnyIPv4              = "0.0.0.0"
	networkBridge            = "bridge"
	namespacePrivate         = "private"
	podmanEnvironmentKey     = "container"
	podmanEnvironmentValue   = "podman"
	cgroupHost               = "host"
	osLinux                  = "linux"
	architectureAMD64        = "amd64"
	architectureARM64        = "arm64"
	variantV8                = "v8"
	restartAlways            = "always"
	restartUnlessStopped     = "unless-stopped"
	restartOnFailure         = "on-failure"
	signalInterruptName      = "SIGINT"
	signalTerminateName      = "SIGTERM"
	stateCreated             = "created"
	stateRunning             = "running"
	statePaused              = "paused"
	stateStopped             = "stopped"
	stateRemoving            = "removing"
	stateUnknown             = "unknown"
	noNewPrivileges          = "no-new-privileges"
	maximumTextBytes         = 4096
)

// CreateOptions contains Libpod create inputs that are intentionally absent
// from the runtime-neutral Spec.
type CreateOptions struct {
	ImageReference   string
	CopyImageVolumes bool
}

// State is the normalized lifecycle from one Libpod inspect document.
type State uint8

const (
	// StateUnknown is the fail-closed zero value.
	StateUnknown State = iota
	// StateCreated has not started.
	StateCreated
	// StateRunning is currently executing.
	StateRunning
	// StatePaused has suspended processes.
	StatePaused
	// StateRemoving is in a transitional delete or stop state.
	StateRemoving
	// StateExited has stopped after starting.
	StateExited
)

// RuntimeMount contains inspect-only identity that cannot be reconstructed
// from portable configuration, including anonymous-volume names and sources.
type RuntimeMount struct {
	Kind     containerconfig.MountKind
	Name     string
	Source   string
	Target   string
	ReadOnly bool
}

// Inspection is the owned result of decoding one native Libpod inspect
// document. Raw labels remain available so callers may interpret their own
// extension or ownership conventions outside this package.
type Inspection struct {
	ID             string
	Name           string
	ImageID        string
	ImageReference string
	ImageDigest    string
	State          State
	Spec           containerconfig.Spec
	RuntimeMounts  []RuntimeMount
	RawLabels      map[string]string
}

// Clone returns a deep copy that callers may mutate without sharing state.
func (inspection Inspection) Clone() Inspection {
	clone := inspection
	clone.Spec = inspection.Spec.Clone()
	clone.RuntimeMounts = slices.Clone(inspection.RuntimeMounts)
	clone.RawLabels = maps.Clone(inspection.RawLabels)

	return clone
}

func validationError(code containerconfig.ValidationCode, path string) error {
	return containerconfig.ValidationError{Code: code, Path: path}
}

func cloneValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value

	return &clone
}
