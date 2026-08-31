package application

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"strconv"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	// AssistProjectionVersion identifies the current LLM-safe projection.
	AssistProjectionVersion = 1
	maximumAssistProjection = 64 << 10
)

// AssistField is one editable field included in an LLM request. Value contains
// only the portable deployment value and never the Compose source text.
type AssistField struct {
	ID          string `json:"id"`
	Value       string `json:"value,omitempty"`
	Present     bool   `json:"present"`
	AllowsUnset bool   `json:"allows_unset"`
	Available   bool   `json:"available"`
}

// AssistProjection is the bounded application-owned input sent to an LLM.
// It deliberately omits image references, environment, credentials, paths,
// runtime object IDs, and fields outside DeploymentFields.
type AssistProjection struct {
	Version      int           `json:"version"`
	Project      string        `json:"project"`
	Service      string        `json:"service"`
	Runtime      string        `json:"runtime"`
	PlatformOS   string        `json:"platform_os"`
	PlatformArch string        `json:"platform_arch"`
	Action       string        `json:"action"`
	Fields       []AssistField `json:"fields"`
	Identity     string        `json:"identity"`
}

// AssistProjectionFor derives the exact bounded projection for one correlated
// snapshot and committed Compose request.
func AssistProjectionFor(
	ctx context.Context,
	request Request,
	snapshot OperationSnapshot,
) (AssistProjection, error) {
	var empty AssistProjection
	if !validEvidenceSnapshot(snapshot) || request.Service != snapshot.Plan.Service {
		return empty, ErrInvalidRequest
	}
	project, err := compose.Load(ctx, request.Source)
	if err != nil {
		return empty, fmt.Errorf("load assist projection: %w", err)
	}
	if project.Name() != snapshot.Plan.Project || assistSourceDigest(request.Source) != snapshot.Plan.Source {
		return empty, ErrSnapshotStale
	}
	spec, err := project.ServiceSpec(request.Service)
	if err != nil {
		return empty, fmt.Errorf("select assist service: %w", err)
	}

	projection := AssistProjection{
		Version: AssistProjectionVersion, Project: snapshot.Plan.Project,
		Service: request.Service, Runtime: snapshot.Plan.Runtime.String(),
		PlatformOS: snapshot.Plan.Platform.OS, PlatformArch: snapshot.Plan.Platform.Architecture,
		Action: string(snapshot.Plan.Kind), Fields: assistFields(spec),
	}

	return finalizeAssistProjection(projection)
}

func assistSourceDigest(source compose.Source) domain.Digest {
	if source.Repository != nil {
		return source.Repository.Digest
	}

	return domain.Hash(source.Content)
}

func finalizeAssistProjection(projection AssistProjection) (AssistProjection, error) {
	encoded, _ := json.Marshal(&projection, json.Deterministic(true))
	projection.Identity = fmt.Sprintf("%x", sha256.Sum256(encoded))
	encoded, _ = json.Marshal(&projection, json.Deterministic(true))
	if len(encoded) > maximumAssistProjection {
		return AssistProjection{}, ErrInvalidRequest
	}

	return projection, nil
}

func assistFields(spec containerconfig.Spec) []AssistField {
	fields := make([]AssistField, 0, len(DeploymentFields()))
	for _, field := range DeploymentFields() {
		fields = append(fields, assistField(spec, field))
	}

	return fields
}

//nolint:cyclop // This is the application-owned exhaustive field projection.
func assistField(spec containerconfig.Spec, field DeploymentField) AssistField {
	result := AssistField{ID: field.ID(), AllowsUnset: field.AllowsUnset(), Available: true}
	switch field {
	case DeploymentCPUs:
		result.Value, result.Present = spec.CPUs, spec.CPUs != ""
	case DeploymentMemory:
		result.Value, result.Present = strconv.FormatInt(spec.MemoryBytes, 10), spec.MemoryBytes != 0
	case DeploymentPIDs:
		result.Value, result.Present = assistOptionalInt64(spec.PidsLimit)
	case DeploymentRestart:
		result.Value, result.Present = spec.Restart, spec.Restart != ""
	case DeploymentSharedMemory:
		result.Value, result.Present = strconv.FormatInt(spec.SharedMemoryBytes, 10), spec.SharedMemoryBytes != 0
	case DeploymentStopGrace:
		if spec.StopTimeout != nil {
			result.Value = (time.Duration(*spec.StopTimeout) * time.Second).String()
			result.Present = true
		}
	case DeploymentInit:
		result.Value, result.Present = assistOptionalBool(spec.Init)
	case DeploymentReadOnly:
		result.Value, result.Present = assistOptionalBool(spec.ReadOnly)
	case DeploymentNoNewPrivileges:
		result.Present = spec.NoNewPrivileges
		result.Available = !spec.NoNewPrivileges
		if result.Present {
			result.Value = "true"
		}
	case DeploymentHealthInterval:
		result.Value, result.Present, result.Available = assistHealth(
			spec.Healthcheck,
			func(value *containerconfig.Healthcheck) string { return value.Interval },
		)
	case DeploymentHealthTimeout:
		result.Value, result.Present, result.Available = assistHealth(
			spec.Healthcheck,
			func(value *containerconfig.Healthcheck) string { return value.Timeout },
		)
	case DeploymentHealthRetries:
		result.Available = spec.Healthcheck != nil && !spec.Healthcheck.Disabled
		if result.Available {
			result.Value, result.Present = assistOptionalInt(spec.Healthcheck.Retries)
		}
	case DeploymentHealthStartPeriod:
		result.Value, result.Present, result.Available = assistHealth(
			spec.Healthcheck,
			func(value *containerconfig.Healthcheck) string { return value.StartPeriod },
		)
	case DeploymentHealthStartInterval:
		result.Value, result.Present, result.Available = assistHealth(
			spec.Healthcheck,
			func(value *containerconfig.Healthcheck) string { return value.StartInterval },
		)
	default:
		result.Available = false
	}

	return result
}

func assistOptionalInt64(value *int64) (string, bool) {
	if value == nil {
		return "", false
	}

	return strconv.FormatInt(*value, 10), true
}

func assistOptionalInt(value *int) (string, bool) {
	if value == nil {
		return "", false
	}

	return strconv.Itoa(*value), true
}

func assistOptionalBool(value *bool) (string, bool) {
	if value == nil {
		return "", false
	}

	return strconv.FormatBool(*value), true
}

func assistHealth(
	health *containerconfig.Healthcheck,
	selectValue func(*containerconfig.Healthcheck) string,
) (string, bool, bool) {
	if health == nil || health.Disabled {
		return "", false, false
	}
	value := selectValue(health)

	return value, value != "", true
}
