package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func executeApply(
	ctx context.Context,
	arguments applyInvocation,
	output io.Writer,
	dependencies applyDependencies,
) error {
	if arguments.dryRun {
		return executeDryRun(ctx, arguments, output, dependencies)
	}

	return executeMutation(ctx, arguments, output, dependencies)
}

func executeDryRun(
	ctx context.Context,
	arguments applyInvocation,
	output io.Writer,
	dependencies applyDependencies,
) error {
	plan, err := prepareApplyPlan(ctx, arguments, dependencies)
	if err != nil {
		return err
	}

	return writeApplyPlan(output, plan, true, arguments.json)
}

func prepareApplyPlan(
	ctx context.Context,
	arguments applyInvocation,
	dependencies applyDependencies,
) (application.Plan, error) {
	request, err := loadApplyRequest(ctx, arguments, dependencies)
	if err != nil {
		return application.Plan{}, err
	}

	plan, err := dependencies.operations.DryRun(ctx, request)
	if err != nil {
		return application.Plan{}, errors.Join(err)
	}

	return plan, nil
}

func executeMutation(
	ctx context.Context,
	arguments applyInvocation,
	output io.Writer,
	dependencies applyDependencies,
) error {
	request, err := loadApplyRequest(ctx, arguments, dependencies)
	if err != nil {
		return err
	}
	plan, err := dependencies.operations.Apply(ctx, request)
	if err != nil {
		return errors.Join(err)
	}

	return writeApplyPlan(output, plan, false, arguments.json)
}

func loadApplyRequest(
	ctx context.Context,
	arguments applyInvocation,
	dependencies applyDependencies,
) (application.Request, error) {
	source, err := dependencies.loadSource(ctx, arguments.compose)
	if err != nil {
		return application.Request{}, fmt.Errorf("load apply source: %w", err)
	}

	return application.Request{Source: source, Service: arguments.service}, nil
}

type applyPlan struct {
	DesiredDigest string         `json:"desired_digest"`
	Image         string         `json:"image"`
	Platform      string         `json:"platform"`
	Project       string         `json:"project"`
	Runtime       string         `json:"runtime"`
	Service       string         `json:"service"`
	SourceDigest  string         `json:"source_digest"`
	Status        string         `json:"status"`
	Warnings      []applyWarning `json:"warnings,omitempty"`
}

type applyWarning struct {
	Code    application.WarningCode `json:"code"`
	Message string                  `json:"message"`
}

func writeApplyPlan(output io.Writer, plan application.Plan, dryRun, jsonOutput bool) error {
	if !jsonOutput {
		return writeHumanApplyPlan(output, plan, dryRun)
	}

	encoded := applyPlan{
		DesiredDigest: plan.Desired.String(),
		Image:         plan.Image.Reference,
		Platform:      platformString(plan.Platform),
		Project:       plan.Project,
		Runtime:       plan.Runtime.String(),
		Service:       plan.Service,
		SourceDigest:  plan.Source.String(),
		Status:        string(plan.Kind),
		Warnings:      applyWarnings(plan.Warnings),
	}

	err := json.NewEncoder(output).Encode(encoded)
	if err != nil {
		return fmt.Errorf("encode apply plan: %w", err)
	}

	return nil
}

func writeHumanApplyPlan(output io.Writer, plan application.Plan, dryRun bool) error {
	result := "Apply completed"
	if dryRun {
		result = "Dry run passed"
	}
	if _, err := fmt.Fprintf(
		output,
		"%s for %s/%s.\nAction: %s (%s).\nRuntime: %s on %s.\nImage: %s.\n",
		result,
		plan.Project,
		plan.Service,
		applyPlanAction(plan.Kind),
		plan.Kind,
		plan.Runtime,
		platformString(plan.Platform),
		plan.Image.Reference,
	); err != nil {
		return fmt.Errorf("write apply result: %w", err)
	}
	for _, warning := range plan.Warnings {
		if _, err := fmt.Fprintf(output, "Warning: %s (%s).\n", warning.Message, warning.Code); err != nil {
			return fmt.Errorf("write apply result: %w", err)
		}
	}
	if dryRun {
		if _, err := io.WriteString(output, "Ready to apply. No changes were made.\n"); err != nil {
			return fmt.Errorf("write apply result: %w", err)
		}
	}

	return nil
}

func applyPlanAction(kind application.PlanKind) string {
	switch kind {
	case application.PlanBootstrap:
		return "create a new workload"
	case application.PlanAdopt:
		return "adopt the matching running workload"
	case application.PlanUnchanged:
		return "keep the matching workload unchanged"
	case application.PlanUpgrade:
		return "upgrade the managed workload"
	case application.PlanResume:
		return "resume the interrupted operation"
	case application.PlanProbeUnknownEffect:
		return "verify an interrupted runtime operation before continuing"
	case application.PlanRestore:
		return "restore the previous workload"
	default:
		return "process the workload"
	}
}

func applyWarnings(warnings []application.Warning) []applyWarning {
	if len(warnings) == 0 {
		return nil
	}

	result := make([]applyWarning, len(warnings))
	for index, warning := range warnings {
		result[index] = applyWarning{Code: warning.Code, Message: warning.Message}
	}

	return result
}

func platformString(platform domain.Platform) string {
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}

	return value
}
