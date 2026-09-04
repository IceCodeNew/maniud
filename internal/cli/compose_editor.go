package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
)

var (
	errDeploymentEditInvalid     = errors.New("deployment edit is invalid")
	errDeploymentPublishFailed   = errors.New("deployment edit publication failed after restore")
	errDeploymentWorktreeUnknown = errors.New("deployment edit worktree outcome is unknown")
)

type deploymentEditCandidate struct {
	source   compose.Source
	fields   []application.DeploymentField
	current  containerconfig.Spec
	proposed containerconfig.Spec
}

//nolint:cyclop // Each branch preserves a separate source or patch proof.
func prepareDeploymentEdit(
	ctx context.Context,
	source compose.Source,
	service string,
	patches []application.DeploymentPatch,
) (deploymentEditCandidate, error) {
	if err := ctx.Err(); err != nil {
		return deploymentEditCandidate{}, fmt.Errorf("prepare deployment edit: %w", err)
	}
	if service == "" || len(patches) == 0 || len(patches) > len(application.DeploymentFields()) {
		return deploymentEditCandidate{}, errDeploymentEditInvalid
	}

	project, err := compose.Load(ctx, source)
	if err != nil {
		return deploymentEditCandidate{}, fmt.Errorf("prepare deployment edit source: %w", err)
	}
	original, err := project.ServiceSpec(service)
	if err != nil {
		return deploymentEditCandidate{}, errDeploymentEditInvalid
	}
	expected, changed, fieldIDs, err := applyDeploymentPatches(original, patches)
	if err != nil {
		return deploymentEditCandidate{}, err
	}
	if len(changed) == 0 {
		return deploymentEditCandidate{source: source}, nil
	}

	candidateSource, err := source.PatchServiceFields(ctx, service, expected, fieldIDs)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return deploymentEditCandidate{}, fmt.Errorf("prepare deployment edit: %w", ctxErr)
		}

		return deploymentEditCandidate{}, errDeploymentEditInvalid
	}

	return deploymentEditCandidate{
		source: candidateSource, fields: changed, current: original, proposed: expected,
	}, nil
}

func applyDeploymentPatches(
	original containerconfig.Spec,
	patches []application.DeploymentPatch,
) (containerconfig.Spec, []application.DeploymentField, []string, error) {
	expected := original
	changed := make([]application.DeploymentField, 0, len(patches))
	fieldIDs := make([]string, 0, len(patches))
	seen := make(map[application.DeploymentField]struct{}, len(patches))
	for _, patch := range patches {
		field := patch.Field()
		if _, duplicate := seen[field]; duplicate {
			return containerconfig.Spec{}, nil, nil, errDeploymentEditInvalid
		}
		seen[field] = struct{}{}
		next, err := patch.ApplyTo(expected)
		if err != nil {
			return containerconfig.Spec{}, nil, nil, errDeploymentEditInvalid
		}
		if containerconfig.Equivalent(expected, next) {
			continue
		}
		expected = next
		changed = append(changed, field)
		fieldIDs = append(fieldIDs, field.ID())
	}

	return expected, changed, fieldIDs, nil
}
