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

	return writeApplyPlan(output, plan)
}

func prepareApplyPlan(
	ctx context.Context,
	arguments applyInvocation,
	dependencies applyDependencies,
) (application.Plan, error) {
	source, err := dependencies.loadSource(ctx, arguments.compose)
	if err != nil {
		return application.Plan{}, fmt.Errorf("load apply source: %w", err)
	}
	runtimeKind, err := applyRuntimeKind(ctx, source, arguments.service)
	if err != nil {
		return application.Plan{}, fmt.Errorf("select apply runtime: %w", err)
	}

	reader, err := dependencies.openReader(ctx)
	if err != nil {
		return application.Plan{}, fmt.Errorf("open apply state: %w", err)
	}

	runtime, err := dependencies.openRuntime(ctx, runtimeKind)
	if err != nil {
		return application.Plan{}, errors.Join(fmt.Errorf("open apply runtime: %w", err), reader.Close())
	}

	plan, runErr := application.NewService(dependencies.images, runtime, reader).DryRun(
		ctx,
		application.Request{Source: source, Service: arguments.service},
	)
	runtime.CloseIdleConnections()

	closeErr := reader.Close()
	if runErr != nil || closeErr != nil {
		return application.Plan{}, errors.Join(runErr, closeErr)
	}

	return plan, nil
}

func executeMutation(
	ctx context.Context,
	arguments applyInvocation,
	output io.Writer,
	dependencies applyDependencies,
) error {
	source, err := dependencies.loadSource(ctx, arguments.compose)
	if err != nil {
		return fmt.Errorf("load apply source: %w", err)
	}
	runtimeKind, err := applyRuntimeKind(ctx, source, arguments.service)
	if err != nil {
		return fmt.Errorf("select apply runtime: %w", err)
	}

	runtime, err := dependencies.openRuntime(ctx, runtimeKind)
	if err != nil {
		return fmt.Errorf("open apply runtime: %w", err)
	}

	state, err := dependencies.openState(ctx)
	if err != nil {
		runtime.CloseIdleConnections()

		return fmt.Errorf("open apply state: %w", err)
	}

	plan, runErr := dependencies.mutate(
		ctx,
		application.Request{Source: source, Service: arguments.service},
		state,
		runtime,
	)
	runtime.CloseIdleConnections()

	closeErr := state.Close()
	if runErr != nil || closeErr != nil {
		return errors.Join(runErr, closeErr)
	}

	return writeApplyPlan(output, plan)
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

func writeApplyPlan(output io.Writer, plan application.Plan) error {
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
