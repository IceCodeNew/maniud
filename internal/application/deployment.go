package application

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
)

var (
	// ErrInvalidDeploymentPatch reports a field or value outside the deployment
	// editor's closed contract.
	ErrInvalidDeploymentPatch = errors.New("deployment patch is invalid")
)

// DeploymentField identifies one portable Compose field that the manual and
// LLM-assisted editors may change.
type DeploymentField uint8

const (
	// DeploymentCPUs changes the service CPU limit.
	DeploymentCPUs DeploymentField = iota + 1
	// DeploymentMemory changes the service memory limit.
	DeploymentMemory
	// DeploymentPIDs changes the service process limit.
	DeploymentPIDs
	// DeploymentRestart changes the service restart policy.
	DeploymentRestart
	// DeploymentSharedMemory changes the service shared-memory size.
	DeploymentSharedMemory
	// DeploymentStopGrace changes the service stop grace period.
	DeploymentStopGrace
	// DeploymentInit enables the service init process.
	DeploymentInit
	// DeploymentReadOnly enables the read-only root filesystem.
	DeploymentReadOnly
	// DeploymentNoNewPrivileges enables the no-new-privileges security option.
	DeploymentNoNewPrivileges
	// DeploymentHealthInterval changes an existing healthcheck interval.
	DeploymentHealthInterval
	// DeploymentHealthTimeout changes an existing healthcheck timeout.
	DeploymentHealthTimeout
	// DeploymentHealthRetries changes an existing healthcheck retry count.
	DeploymentHealthRetries
	// DeploymentHealthStartPeriod changes an existing healthcheck start period.
	DeploymentHealthStartPeriod
	// DeploymentHealthStartInterval changes an existing healthcheck start interval.
	DeploymentHealthStartInterval
)

// DeploymentFields returns every editable field in stable display order.
func DeploymentFields() []DeploymentField {
	return []DeploymentField{
		DeploymentCPUs,
		DeploymentMemory,
		DeploymentPIDs,
		DeploymentRestart,
		DeploymentSharedMemory,
		DeploymentStopGrace,
		DeploymentInit,
		DeploymentReadOnly,
		DeploymentNoNewPrivileges,
		DeploymentHealthInterval,
		DeploymentHealthTimeout,
		DeploymentHealthRetries,
		DeploymentHealthStartPeriod,
		DeploymentHealthStartInterval,
	}
}

// ParseDeploymentField resolves one stable field ID.
func ParseDeploymentField(identifier string) (DeploymentField, error) {
	for _, field := range DeploymentFields() {
		if field.ID() == identifier {
			return field, nil
		}
	}

	return 0, ErrInvalidDeploymentPatch
}

// ID returns the stable field identifier used by projections and commit
// metadata. An unknown field has no ID.
//
//nolint:cyclop // Each closed field owns one stable external identifier.
func (field DeploymentField) ID() string {
	switch field {
	case DeploymentCPUs:
		return "cpus"
	case DeploymentMemory:
		return "mem_limit"
	case DeploymentPIDs:
		return "pids_limit"
	case DeploymentRestart:
		return "restart"
	case DeploymentSharedMemory:
		return "shm_size"
	case DeploymentStopGrace:
		return "stop_grace_period"
	case DeploymentInit:
		return "init"
	case DeploymentReadOnly:
		return "read_only"
	case DeploymentNoNewPrivileges:
		return "no_new_privileges"
	case DeploymentHealthInterval:
		return "healthcheck.interval"
	case DeploymentHealthTimeout:
		return "healthcheck.timeout"
	case DeploymentHealthRetries:
		return "healthcheck.retries"
	case DeploymentHealthStartPeriod:
		return "healthcheck.start_period"
	case DeploymentHealthStartInterval:
		return "healthcheck.start_interval"
	default:
		return ""
	}
}

// AllowsUnset reports whether the field may be restored to Compose defaults.
// No-new-privileges remains a one-way editor operation.
func (field DeploymentField) AllowsUnset() bool {
	return field.ID() != "" && field != DeploymentNoNewPrivileges
}

// DeploymentValue is the closed value family accepted by DeploymentPatch.
// Callers construct one of the concrete application-owned value types below.
type DeploymentValue interface {
	deploymentValue()
}

// DeploymentCPU is a positive CPU count with Compose's float32 precision.
type DeploymentCPU float32

func (DeploymentCPU) deploymentValue() {}

// DeploymentBytes is a positive byte count.
type DeploymentBytes int64

func (DeploymentBytes) deploymentValue() {}

// DeploymentInteger is an integer process limit.
type DeploymentInteger int64

func (DeploymentInteger) deploymentValue() {}

// DeploymentRetries is a healthcheck retry count bounded to Compose's int representation.
type DeploymentRetries int

func (DeploymentRetries) deploymentValue() {}

// DeploymentRestartPolicy is one portable restart policy.
type DeploymentRestartPolicy string

func (DeploymentRestartPolicy) deploymentValue() {}

// DeploymentDuration is one positive deployment duration.
type DeploymentDuration time.Duration

func (DeploymentDuration) deploymentValue() {}

// DeploymentBoolean sets one ordinary boolean deployment field.
type DeploymentBoolean bool

func (DeploymentBoolean) deploymentValue() {}

// DeploymentEnabled enables the one-way no-new-privileges field.
type DeploymentEnabled struct{}

func (DeploymentEnabled) deploymentValue() {}

// DeploymentUnset removes one optional deployment field.
type DeploymentUnset struct{}

func (DeploymentUnset) deploymentValue() {}

// DeploymentPatch is one validated field mutation. Its private representation
// prevents callers from pairing a field with the wrong value family.
type DeploymentPatch struct {
	field DeploymentField
	value DeploymentValue
}

// NewDeploymentPatch validates and binds one typed field mutation.
func NewDeploymentPatch(field DeploymentField, value DeploymentValue) (DeploymentPatch, error) {
	if !validDeploymentValue(field, value) {
		return DeploymentPatch{}, ErrInvalidDeploymentPatch
	}

	return DeploymentPatch{field: field, value: value}, nil
}

// Field returns the field changed by the patch.
func (patch DeploymentPatch) Field() DeploymentField {
	return patch.field
}

// ApplyTo returns a cloned portable specification with the patch applied.
func (patch DeploymentPatch) ApplyTo(spec containerconfig.Spec) (containerconfig.Spec, error) {
	if !validDeploymentValue(patch.field, patch.value) {
		return containerconfig.Spec{}, ErrInvalidDeploymentPatch
	}
	result := spec.Clone()
	if isHealthField(patch.field) && (result.Healthcheck == nil || result.Healthcheck.Disabled) {
		return containerconfig.Spec{}, ErrInvalidDeploymentPatch
	}

	if _, unset := patch.value.(DeploymentUnset); unset {
		unsetDeploymentField(&result, patch.field)

		return containerconfig.Canonical(result), nil
	}
	setDeploymentField(&result, patch)

	return containerconfig.Canonical(result), nil
}

//nolint:cyclop // Every closed field validates a distinct typed value contract.
func validDeploymentValue(field DeploymentField, value DeploymentValue) bool {
	if value == nil {
		return false
	}
	if _, unset := value.(DeploymentUnset); unset {
		return field.AllowsUnset()
	}

	switch field {
	case DeploymentCPUs:
		cpu, valid := value.(DeploymentCPU)

		return valid && cpu > 0 && !math.IsInf(float64(cpu), 0) && !math.IsNaN(float64(cpu)) &&
			float64(cpu) <= float64(math.MaxInt64)/1_000_000_000
	case DeploymentMemory, DeploymentSharedMemory:
		bytes, valid := value.(DeploymentBytes)

		return valid && bytes > 0
	case DeploymentPIDs:
		integer, valid := value.(DeploymentInteger)

		return valid && (integer == -1 || integer > 0)
	case DeploymentRestart:
		restart, valid := value.(DeploymentRestartPolicy)

		return valid && validDeploymentRestart(string(restart))
	case DeploymentStopGrace:
		duration, valid := value.(DeploymentDuration)

		return valid && duration > 0 && time.Duration(duration)%time.Second == 0
	case DeploymentInit, DeploymentReadOnly:
		_, valid := value.(DeploymentBoolean)

		return valid
	case DeploymentNoNewPrivileges:
		_, valid := value.(DeploymentEnabled)

		return valid
	case DeploymentHealthRetries:
		retries, valid := value.(DeploymentRetries)

		return valid && retries > 0
	case DeploymentHealthInterval, DeploymentHealthTimeout,
		DeploymentHealthStartPeriod, DeploymentHealthStartInterval:
		duration, valid := value.(DeploymentDuration)

		return valid && duration > 0
	default:
		return false
	}
}

func validDeploymentRestart(value string) bool {
	switch value {
	case "no", "always", "unless-stopped", "on-failure":
		return true
	default:
		retries, found := strings.CutPrefix(value, "on-failure:")
		parsed, err := strconv.ParseUint(retries, 10, 31)

		return found && err == nil && parsed > 0
	}
}

func isHealthField(field DeploymentField) bool {
	return field >= DeploymentHealthInterval && field <= DeploymentHealthStartInterval
}

//nolint:cyclop // Every optional field clears a distinct Spec representation.
func unsetDeploymentField(spec *containerconfig.Spec, field DeploymentField) {
	switch field {
	case DeploymentCPUs:
		spec.CPUs = ""
	case DeploymentMemory:
		spec.MemoryBytes = 0
	case DeploymentPIDs:
		spec.PidsLimit = nil
	case DeploymentRestart:
		spec.Restart = ""
	case DeploymentSharedMemory:
		spec.SharedMemoryBytes = 0
	case DeploymentStopGrace:
		spec.StopTimeout = nil
	case DeploymentInit:
		spec.Init = nil
	case DeploymentReadOnly:
		spec.ReadOnly = nil
	case DeploymentNoNewPrivileges:
	case DeploymentHealthInterval:
		spec.Healthcheck.Interval = ""
	case DeploymentHealthTimeout:
		spec.Healthcheck.Timeout = ""
	case DeploymentHealthRetries:
		spec.Healthcheck.Retries = nil
	case DeploymentHealthStartPeriod:
		spec.Healthcheck.StartPeriod = ""
	case DeploymentHealthStartInterval:
		spec.Healthcheck.StartInterval = ""
	}
}

//nolint:cyclop,forcetypeassert // ApplyTo validates the private field/value pair before dispatch.
func setDeploymentField(spec *containerconfig.Spec, patch DeploymentPatch) {
	switch patch.field {
	case DeploymentCPUs:
		value := patch.value.(DeploymentCPU)
		spec.CPUs = strconv.FormatFloat(float64(value), 'f', -1, 32)
	case DeploymentMemory:
		spec.MemoryBytes = int64(patch.value.(DeploymentBytes))
	case DeploymentPIDs:
		spec.PidsLimit = new(int64(patch.value.(DeploymentInteger)))
	case DeploymentRestart:
		spec.Restart = string(patch.value.(DeploymentRestartPolicy))
	case DeploymentSharedMemory:
		spec.SharedMemoryBytes = int64(patch.value.(DeploymentBytes))
	case DeploymentStopGrace:
		seconds := int64(time.Duration(patch.value.(DeploymentDuration)) / time.Second)
		spec.StopTimeout = new(seconds)
	case DeploymentInit:
		spec.Init = new(bool(patch.value.(DeploymentBoolean)))
	case DeploymentReadOnly:
		spec.ReadOnly = new(bool(patch.value.(DeploymentBoolean)))
	case DeploymentNoNewPrivileges:
		spec.NoNewPrivileges = true
	case DeploymentHealthInterval:
		spec.Healthcheck.Interval = time.Duration(patch.value.(DeploymentDuration)).String()
	case DeploymentHealthTimeout:
		spec.Healthcheck.Timeout = time.Duration(patch.value.(DeploymentDuration)).String()
	case DeploymentHealthRetries:
		spec.Healthcheck.Retries = new(int(patch.value.(DeploymentRetries)))
	case DeploymentHealthStartPeriod:
		spec.Healthcheck.StartPeriod = time.Duration(patch.value.(DeploymentDuration)).String()
	case DeploymentHealthStartInterval:
		spec.Healthcheck.StartInterval = time.Duration(patch.value.(DeploymentDuration)).String()
	}
}
