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

// DeploymentPatch is one validated field mutation. Its private representation
// prevents callers from bypassing the shared external value grammar.
type DeploymentPatch struct {
	field DeploymentField
	value string
	unset bool
}

// ParseDeploymentPatch parses one external field/value pair into the same
// patch used by manual and LLM-assisted deployment edits.
func ParseDeploymentPatch(fieldID string, value string, unset bool) (DeploymentPatch, error) {
	field, err := ParseDeploymentField(fieldID)
	if err != nil {
		return DeploymentPatch{}, ErrInvalidDeploymentPatch
	}
	if unset {
		if !field.AllowsUnset() {
			return DeploymentPatch{}, ErrInvalidDeploymentPatch
		}

		return DeploymentPatch{field: field, unset: true}, nil
	}
	patch := DeploymentPatch{field: field, value: value}
	if !validDeploymentPatchStructure(patch) {
		return DeploymentPatch{}, ErrInvalidDeploymentPatch
	}
	validation := containerconfig.Spec{Healthcheck: new(containerconfig.Healthcheck)}
	if err = setDeploymentField(&validation, patch.field, patch.value); err != nil {
		return DeploymentPatch{}, ErrInvalidDeploymentPatch
	}

	return patch, nil
}

// Field returns the field changed by the patch.
func (patch DeploymentPatch) Field() DeploymentField {
	return patch.field
}

// ApplyTo returns a cloned portable specification with the patch applied.
func (patch DeploymentPatch) ApplyTo(spec containerconfig.Spec) (containerconfig.Spec, error) {
	if !validDeploymentPatchStructure(patch) {
		return containerconfig.Spec{}, ErrInvalidDeploymentPatch
	}
	result := spec.Clone()
	if isHealthField(patch.field) && (result.Healthcheck == nil || result.Healthcheck.Disabled) {
		return containerconfig.Spec{}, ErrInvalidDeploymentPatch
	}

	if patch.unset {
		unsetDeploymentField(&result, patch.field)

		return containerconfig.Canonical(result), nil
	}
	if err := setDeploymentField(&result, patch.field, patch.value); err != nil {
		return containerconfig.Spec{}, ErrInvalidDeploymentPatch
	}

	return containerconfig.Canonical(result), nil
}

func validDeploymentPatchStructure(patch DeploymentPatch) bool {
	if patch.field.ID() == "" {
		return false
	}
	if patch.unset {
		return patch.value == "" && patch.field.AllowsUnset()
	}
	if patch.value == "" || strings.TrimSpace(patch.value) != patch.value ||
		strings.ContainsAny(patch.value, "\x00\r\n") {
		return false
	}

	return true
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

//nolint:cyclop,funlen,gocognit,gocyclo // Every closed field owns one value grammar and Spec representation.
func setDeploymentField(spec *containerconfig.Spec, field DeploymentField, value string) error {
	switch field {
	case DeploymentCPUs:
		number, err := strconv.ParseFloat(value, 32)
		if err != nil || number <= 0 || math.IsInf(number, 0) || math.IsNaN(number) ||
			number > float64(math.MaxInt64)/1_000_000_000 {
			return ErrInvalidDeploymentPatch
		}
		spec.CPUs = strconv.FormatFloat(number, 'f', -1, 32)
	case DeploymentMemory:
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number <= 0 {
			return ErrInvalidDeploymentPatch
		}
		spec.MemoryBytes = number
	case DeploymentPIDs:
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || (number != -1 && number <= 0) {
			return ErrInvalidDeploymentPatch
		}
		spec.PidsLimit = new(number)
	case DeploymentRestart:
		if !validDeploymentRestart(value) {
			return ErrInvalidDeploymentPatch
		}
		spec.Restart = value
	case DeploymentSharedMemory:
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number <= 0 {
			return ErrInvalidDeploymentPatch
		}
		spec.SharedMemoryBytes = number
	case DeploymentStopGrace:
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 || duration%time.Second != 0 {
			return ErrInvalidDeploymentPatch
		}
		seconds := int64(duration / time.Second)
		spec.StopTimeout = new(seconds)
	case DeploymentInit:
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return ErrInvalidDeploymentPatch
		}
		spec.Init = new(enabled)
	case DeploymentReadOnly:
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return ErrInvalidDeploymentPatch
		}
		spec.ReadOnly = new(enabled)
	case DeploymentNoNewPrivileges:
		if value != "true" {
			return ErrInvalidDeploymentPatch
		}
		spec.NoNewPrivileges = true
	case DeploymentHealthInterval:
		duration, err := positiveDeploymentDuration(value)
		if err != nil {
			return err
		}
		spec.Healthcheck.Interval = duration.String()
	case DeploymentHealthTimeout:
		duration, err := positiveDeploymentDuration(value)
		if err != nil {
			return err
		}
		spec.Healthcheck.Timeout = duration.String()
	case DeploymentHealthRetries:
		retries, err := strconv.Atoi(value)
		if err != nil || retries <= 0 {
			return ErrInvalidDeploymentPatch
		}
		spec.Healthcheck.Retries = new(retries)
	case DeploymentHealthStartPeriod:
		duration, err := positiveDeploymentDuration(value)
		if err != nil {
			return err
		}
		spec.Healthcheck.StartPeriod = duration.String()
	case DeploymentHealthStartInterval:
		duration, err := positiveDeploymentDuration(value)
		if err != nil {
			return err
		}
		spec.Healthcheck.StartInterval = duration.String()
	default:
		return ErrInvalidDeploymentPatch
	}

	return nil
}

func positiveDeploymentDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, ErrInvalidDeploymentPatch
	}

	return duration, nil
}
