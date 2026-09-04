package tui

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

func canonicalCatalog(snapshot CatalogSnapshot) CatalogSnapshot {
	suggested, err := canonicalDisplay(snapshot.SuggestedRepository)
	if err != nil {
		snapshot.State = CatalogUnavailable
		snapshot.SuggestedRepository = ""
	} else {
		snapshot.SuggestedRepository = suggested
	}
	services := make([]Service, 0, len(snapshot.Services))
	for _, service := range snapshot.Services {
		location, locationErr := canonicalDisplay(service.Location)
		project, projectErr := canonicalDisplay(service.Project)
		name, nameErr := canonicalDisplay(service.Name)
		runtimeName, runtimeErr := canonicalDisplay(service.Runtime)
		if locationErr != nil || projectErr != nil || nameErr != nil || runtimeErr != nil {
			services = append(services, Service{ID: service.ID, Location: "Invalid source", Blocker: BlockerInvalid})

			continue
		}
		services = append(services, Service{
			ID: service.ID, Location: location, Project: project, Name: name,
			Runtime: runtimeName, Blocker: service.Blocker,
		})
		if diagnostic, valid := canonicalSourceDiagnostic(service.Diagnostic); valid && service.Blocker == BlockerInvalid {
			services[len(services)-1].Diagnostic = diagnostic
		}
	}
	snapshot.Services = services

	return snapshot
}

func canonicalSourceDiagnostic(diagnostic SourceDiagnostic) (SourceDiagnostic, bool) {
	file, valid := canonicalSourceDiagnosticFile(diagnostic.File)
	if !valid || !validSourceDiagnosticPosition(diagnostic.Line, diagnostic.Column) ||
		!validSourceDiagnosticReason(diagnostic.Reason) {
		return SourceDiagnostic{}, false
	}
	diagnostic.File = file

	return diagnostic, true
}

func canonicalSourceDiagnosticFile(file string) (string, bool) {
	canonical, err := canonicalDisplay(file)
	native := filepath.FromSlash(canonical)
	if err != nil || canonical == "" || filepath.IsAbs(native) || !filepath.IsLocal(native) ||
		filepath.ToSlash(filepath.Clean(native)) != canonical {
		return "", false
	}

	return canonical, true
}

func validSourceDiagnosticPosition(line, column int) bool {
	return line >= 0 && column >= 0 && (line != 0 || column == 0)
}

func validSourceDiagnosticReason(reason SourceDiagnosticReason) bool {
	switch reason {
	case DiagnosticYAMLSyntax, DiagnosticYAMLStructure, DiagnosticYAMLUnsupported, DiagnosticComposeValidation:
		return true
	default:
		return false
	}
}

func canonicalChoices(targets []Target) ([]serviceChoice, error) {
	choices := make([]serviceChoice, 0, len(targets))
	for _, target := range targets {
		project, err := canonicalDisplay(target.Project)
		if err != nil {
			return nil, err
		}
		service, err := canonicalDisplay(target.Service)
		if err != nil {
			return nil, err
		}
		runtimeName, err := canonicalDisplay(target.Runtime)
		if err != nil {
			return nil, err
		}
		choices = append(choices, serviceChoice{
			project: project, service: service, runtime: runtimeName, request: target.Request,
		})
	}

	return choices, nil
}

func projectPlan(snapshot application.OperationSnapshot) (planView, error) {
	plan := snapshot.Plan
	if !slices.Contains([]application.HealthConvergence{
		application.HealthConvergenceNone,
		application.HealthConvergencePending,
		application.HealthConvergenceHealthy,
		application.HealthConvergenceDegraded,
	}, plan.Health) || !validProjectedHealthResolution(snapshot) {
		return planView{}, errInvalidInput
	}
	current := "Not deployed"
	if snapshot.HasApplied {
		current = snapshot.Applied.Reference
	}
	raw := []string{
		plan.Project,
		plan.Service,
		plan.Runtime.String(),
		platformName(plan),
		current,
		plan.Image.Reference,
		string(plan.Kind),
	}
	values := make([]string, len(raw))
	for index, value := range raw {
		canonical, err := canonicalDisplay(value)
		if err != nil {
			return planView{}, err
		}
		values[index] = canonical
	}

	view := planView{
		kind: values[6], project: values[0], service: values[1], runtime: values[2],
		platform: values[3], current: values[4], proposed: values[5],
		status: statusReady,
	}
	if snapshot.HasTransaction {
		view.transaction = snapshot.Transaction.ID
	}
	view.resolution = snapshot.AvailableHealthResolution
	view.restoresPrevious = snapshot.HealthResolutionRestoresPrevious
	projectHealthPlan(snapshot, &view)
	if plan.Kind == application.PlanUnchanged {
		view.status = "No runtime change needed"
	}
	if len(plan.Warnings) > 0 {
		view.warningText = fmt.Sprintf("%d warning(s) require review", len(plan.Warnings))
	}

	return view, nil
}

func projectHealthPlan(snapshot application.OperationSnapshot, view *planView) {
	plan := snapshot.Plan
	view.health = plan.Health
	view.healthPoll = plan.HealthPoll
	view.healthFails = plan.Observation.Health.FailingStreak
	view.healthState = string(plan.Observation.Health.Status)
	view.healthProof = application.HealthResolutionObservation{
		State: plan.Observation.State, WorkloadID: plan.Observation.ID,
		StartedAt:            plan.Observation.StartedAt,
		Configuration:        plan.Observation.ConfigurationDigest,
		Storage:              plan.Observation.StorageDigest,
		ConfigurationMatches: plan.Observation.ConfigurationMatches,
		Lifecycle:            plan.Observation.Lifecycle,
		Health:               plan.Observation.Health,
		Ownership:            plan.Observation.Ownership,
	}
	if plan.Observation.Lifecycle == application.WorkloadLifecycleRestarting {
		view.healthState = "restarting"
	}
	//nolint:exhaustive // projectPlan rejects values outside the closed enum before this projection.
	switch plan.Health {
	case application.HealthConvergenceNone:
		view.status = statusReady
	case application.HealthConvergencePending:
		view.status = "Waiting for workload health"
	case application.HealthConvergenceHealthy:
		view.status = "Workload health recovered"
	default:
		view.status = "Workload health requires a decision"
	}
}

func validProjectedHealthResolution(snapshot application.OperationSnapshot) bool {
	if snapshot.AvailableHealthResolution == "" {
		return !snapshot.HealthResolutionRestoresPrevious
	}
	if !snapshot.HasTransaction || snapshot.Transaction.ID == "" {
		return false
	}
	if snapshot.HealthResolutionRestoresPrevious {
		return snapshot.AvailableHealthResolution == application.HealthResolutionRollback
	}

	return snapshot.AvailableHealthResolution == application.HealthResolutionRollback ||
		snapshot.AvailableHealthResolution == application.HealthResolutionCancelAdoption ||
		snapshot.AvailableHealthResolution == application.HealthResolutionRetryRestoreStart
}

func platformName(plan application.Plan) string {
	value := plan.Platform.OS + "/" + plan.Platform.Architecture
	if plan.Platform.Variant != "" {
		value += "/" + plan.Platform.Variant
	}

	return value
}

func canonicalDisplay(value string) (string, error) {
	canonical, err := terminaltext.Canonicalize(value, displayLimits())
	if err != nil {
		return "", fmt.Errorf("canonicalize display text: %w", err)
	}

	return canonical, nil
}

func catalogMessage(snapshot CatalogSnapshot) string {
	switch snapshot.State {
	case CatalogReady:
		if len(snapshot.Services) == 0 {
			return "No registered services"
		}

		return "Choose a service"
	case CatalogMissing:
		return "No registered repository"
	case CatalogUnavailable:
		return "Registered services unavailable"
	default:
		return "Registered services unavailable"
	}
}

func blockerMessage(blocker SourceBlocker) string {
	switch blocker {
	case BlockerNone, BlockerInvalid:
		return "Compose source did not pass validation"
	case BlockerNotFound:
		return "Compose source is no longer registered"
	case BlockerUnavailable:
		return "Compose source is unavailable"
	default:
		return "Compose source did not pass validation"
	}
}
