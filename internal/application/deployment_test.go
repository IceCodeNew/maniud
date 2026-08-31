//nolint:goconst,lll // Closed-contract matrices keep complete field/value expectations adjacent.
package application

import (
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
)

//nolint:cyclop // Stable identifier assertions cover the complete closed field set.
func TestDeploymentFieldsUseStableClosedIdentifiers(t *testing.T) {
	t.Parallel()

	want := []string{
		"cpus", "mem_limit", "pids_limit", "restart", "shm_size", "stop_grace_period",
		"init", "read_only", "no_new_privileges", "healthcheck.interval", "healthcheck.timeout",
		"healthcheck.retries", "healthcheck.start_period", "healthcheck.start_interval",
	}
	fields := DeploymentFields()
	if len(fields) != len(want) {
		t.Fatalf("DeploymentFields() = %v", fields)
	}
	for index, identifier := range want {
		field, err := ParseDeploymentField(identifier)
		if err != nil || field != fields[index] || field.ID() != identifier {
			t.Fatalf("ParseDeploymentField(%q) = %v, %v", identifier, field, err)
		}
	}
	fields[0] = 0
	if DeploymentFields()[0] != DeploymentCPUs {
		t.Fatal("DeploymentFields() shared its backing array")
	}
	if _, err := ParseDeploymentField("unknown"); !errors.Is(err, ErrInvalidDeploymentPatch) {
		t.Fatalf("ParseDeploymentField(unknown) error = %v", err)
	}
	if DeploymentField(255).ID() != "" || DeploymentField(255).AllowsUnset() ||
		DeploymentNoNewPrivileges.AllowsUnset() {
		t.Fatal("unknown or one-way field allowed unset")
	}
}

//nolint:cyclop // The table proves every typed field representation.
func TestDeploymentPatchAppliesEveryTypedField(t *testing.T) {
	t.Parallel()

	retries := 2
	baseline := containerconfig.Spec{Healthcheck: &containerconfig.Healthcheck{
		Test: []string{"CMD", "true"}, Interval: "1s", Timeout: "2s", Retries: &retries,
		StartPeriod: "3s", StartInterval: "4s",
	}}
	tests := []struct {
		name  string
		field DeploymentField
		value DeploymentValue
		check func(containerconfig.Spec) bool
	}{
		{"cpus", DeploymentCPUs, DeploymentCPU(1.5), func(spec containerconfig.Spec) bool { return spec.CPUs == "1.5" }},
		{"memory", DeploymentMemory, DeploymentBytes(512), func(spec containerconfig.Spec) bool { return spec.MemoryBytes == 512 }},
		{"pids", DeploymentPIDs, DeploymentInteger(-1), func(spec containerconfig.Spec) bool { return spec.PidsLimit != nil && *spec.PidsLimit == -1 }},
		{"restart", DeploymentRestart, DeploymentRestartPolicy("on-failure:3"), func(spec containerconfig.Spec) bool { return spec.Restart == "on-failure:3" }},
		{"shared memory", DeploymentSharedMemory, DeploymentBytes(256), func(spec containerconfig.Spec) bool { return spec.SharedMemoryBytes == 256 }},
		{"stop grace", DeploymentStopGrace, DeploymentDuration(5 * time.Second), func(spec containerconfig.Spec) bool { return spec.StopTimeout != nil && *spec.StopTimeout == 5 }},
		{"init", DeploymentInit, DeploymentBoolean(true), func(spec containerconfig.Spec) bool { return spec.Init != nil && *spec.Init }},
		{"read only", DeploymentReadOnly, DeploymentBoolean(false), func(spec containerconfig.Spec) bool { return spec.ReadOnly != nil && !*spec.ReadOnly }},
		{"no new privileges", DeploymentNoNewPrivileges, DeploymentEnabled{}, func(spec containerconfig.Spec) bool { return spec.NoNewPrivileges }},
		{"health interval", DeploymentHealthInterval, DeploymentDuration(5 * time.Second), func(spec containerconfig.Spec) bool { return spec.Healthcheck.Interval == "5s" }},
		{"health timeout", DeploymentHealthTimeout, DeploymentDuration(6 * time.Second), func(spec containerconfig.Spec) bool { return spec.Healthcheck.Timeout == "6s" }},
		{"health retries", DeploymentHealthRetries, DeploymentRetries(7), func(spec containerconfig.Spec) bool {
			return spec.Healthcheck.Retries != nil && *spec.Healthcheck.Retries == 7
		}},
		{"health start period", DeploymentHealthStartPeriod, DeploymentDuration(8 * time.Second), func(spec containerconfig.Spec) bool { return spec.Healthcheck.StartPeriod == "8s" }},
		{"health start interval", DeploymentHealthStartInterval, DeploymentDuration(9 * time.Second), func(spec containerconfig.Spec) bool { return spec.Healthcheck.StartInterval == "9s" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			patch, err := NewDeploymentPatch(test.field, test.value)
			if err != nil || patch.Field() != test.field {
				t.Fatalf("NewDeploymentPatch() = %#v, %v", patch, err)
			}
			got, err := patch.ApplyTo(baseline)
			if err != nil || !test.check(got) || !slices.Equal(baseline.Healthcheck.Test, []string{"CMD", "true"}) {
				t.Fatalf("ApplyTo() = %#v, %v", got, err)
			}
		})
	}
}

func TestDeploymentPatchUnsetsOptionalFields(t *testing.T) {
	t.Parallel()

	retries := 3
	value := int64(10)
	baseline := containerconfig.Spec{
		CPUs: "1", MemoryBytes: 1, PidsLimit: &value, Restart: "always", SharedMemoryBytes: 1,
		StopTimeout: &value, Init: new(true), ReadOnly: new(true),
		Healthcheck: &containerconfig.Healthcheck{
			Test: []string{"CMD", "true"}, Interval: "1s", Timeout: "1s", Retries: &retries,
			StartPeriod: "1s", StartInterval: "1s",
		},
	}
	for _, field := range DeploymentFields() {
		if !field.AllowsUnset() {
			continue
		}
		patch, err := NewDeploymentPatch(field, DeploymentUnset{})
		if err != nil {
			t.Fatalf("NewDeploymentPatch(%s, unset) error = %v", field.ID(), err)
		}
		got, err := patch.ApplyTo(baseline)
		if err != nil || deploymentFieldPresent(got, field) {
			t.Fatalf("ApplyTo(unset %s) = %#v, %v", field.ID(), got, err)
		}
	}
}

func TestDeploymentPatchRejectsInvalidPairingsAndHealthState(t *testing.T) {
	t.Parallel()

	invalid := []struct {
		field DeploymentField
		value DeploymentValue
	}{
		{0, DeploymentCPU(1)},
		{DeploymentCPUs, nil},
		{DeploymentCPUs, DeploymentCPU(0)},
		{DeploymentCPUs, DeploymentCPU(float32(math.Inf(1)))},
		{DeploymentCPUs, DeploymentBytes(1)},
		{DeploymentMemory, DeploymentBytes(0)},
		{DeploymentPIDs, DeploymentInteger(0)},
		{DeploymentField(255), DeploymentInteger(1)},
		{DeploymentRestart, DeploymentRestartPolicy("on-failure:0")},
		{DeploymentRestart, DeploymentRestartPolicy("sometimes")},
		{DeploymentStopGrace, DeploymentDuration(time.Millisecond)},
		{DeploymentInit, DeploymentInteger(1)},
		{DeploymentNoNewPrivileges, DeploymentUnset{}},
		{DeploymentHealthRetries, DeploymentInteger(0)},
		{DeploymentHealthRetries, DeploymentRetries(0)},
		{DeploymentHealthInterval, DeploymentDuration(0)},
	}
	for _, test := range invalid {
		if _, err := NewDeploymentPatch(test.field, test.value); !errors.Is(err, ErrInvalidDeploymentPatch) {
			t.Fatalf("NewDeploymentPatch(%v, %#v) error = %v", test.field, test.value, err)
		}
	}

	patch, err := NewDeploymentPatch(DeploymentHealthTimeout, DeploymentDuration(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []containerconfig.Spec{
		{},
		{Healthcheck: &containerconfig.Healthcheck{Disabled: true}},
	} {
		if _, applyErr := patch.ApplyTo(spec); !errors.Is(applyErr, ErrInvalidDeploymentPatch) {
			t.Fatalf("ApplyTo(%#v) error = %v", spec, applyErr)
		}
	}
	if _, err = (DeploymentPatch{}).ApplyTo(containerconfig.Spec{}); !errors.Is(err, ErrInvalidDeploymentPatch) {
		t.Fatalf("ApplyTo(zero patch) error = %v", err)
	}
}

func TestDeploymentValueFamilyAndRestartPolicies(t *testing.T) {
	t.Parallel()

	DeploymentCPU(1).deploymentValue()
	DeploymentBytes(1).deploymentValue()
	DeploymentInteger(1).deploymentValue()
	DeploymentRetries(1).deploymentValue()
	DeploymentRestartPolicy("always").deploymentValue()
	DeploymentDuration(time.Second).deploymentValue()
	DeploymentBoolean(true).deploymentValue()
	(DeploymentEnabled{}).deploymentValue()
	(DeploymentUnset{}).deploymentValue()

	for _, policy := range []DeploymentRestartPolicy{"no", "always", "unless-stopped", "on-failure"} {
		if _, err := NewDeploymentPatch(DeploymentRestart, policy); err != nil {
			t.Fatalf("NewDeploymentPatch(restart %q) error = %v", policy, err)
		}
	}
}

func TestDeploymentDispatchIgnoresValuesOutsideTheClosedContract(t *testing.T) {
	t.Parallel()

	spec := containerconfig.Spec{NoNewPrivileges: true}
	unsetDeploymentField(&spec, DeploymentNoNewPrivileges)
	unsetDeploymentField(&spec, DeploymentField(255))
	setDeploymentField(&spec, DeploymentPatch{field: DeploymentField(255), value: DeploymentInteger(1)})
	if !spec.NoNewPrivileges {
		t.Fatal("closed deployment dispatch cleared no-new-privileges")
	}
}

//nolint:cyclop // Test helper mirrors every optional field representation.
func deploymentFieldPresent(spec containerconfig.Spec, field DeploymentField) bool {
	switch field {
	case DeploymentCPUs:
		return spec.CPUs != ""
	case DeploymentMemory:
		return spec.MemoryBytes != 0
	case DeploymentPIDs:
		return spec.PidsLimit != nil
	case DeploymentRestart:
		return spec.Restart != ""
	case DeploymentSharedMemory:
		return spec.SharedMemoryBytes != 0
	case DeploymentStopGrace:
		return spec.StopTimeout != nil
	case DeploymentInit:
		return spec.Init != nil
	case DeploymentReadOnly:
		return spec.ReadOnly != nil
	case DeploymentNoNewPrivileges:
		return spec.NoNewPrivileges
	case DeploymentHealthInterval:
		return spec.Healthcheck.Interval != ""
	case DeploymentHealthTimeout:
		return spec.Healthcheck.Timeout != ""
	case DeploymentHealthRetries:
		return spec.Healthcheck.Retries != nil
	case DeploymentHealthStartPeriod:
		return spec.Healthcheck.StartPeriod != ""
	case DeploymentHealthStartInterval:
		return spec.Healthcheck.StartInterval != ""
	default:
		return true
	}
}
