package cli

import (
	"context"
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

type tuiAssistContext struct {
	projection application.AssistProjection
	identity   string
	forbidden  map[string][]string
}

func (workspace *tuiDeploymentWorkspace) assistContext(
	ctx context.Context,
	request application.Request,
	operations tui.Operations,
) (tuiAssistContext, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if operations == nil || workspace.draft != nil || workspace.staged != nil {
		return tuiAssistContext{}, errDeploymentEditInvalid
	}

	return workspace.captureAssistContext(ctx, request, operations)
}
func (workspace *tuiDeploymentWorkspace) pendingAssistContext(
	ctx context.Context,
	request application.Request,
	operations tui.Operations,
) (tuiAssistContext, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if operations == nil || workspace.staged != nil {
		return tuiAssistContext{}, errDeploymentEditInvalid
	}

	return workspace.captureAssistContext(ctx, request, operations)
}
func (workspace *tuiDeploymentWorkspace) captureAssistContext(
	ctx context.Context,
	request application.Request,
	operations tui.Operations,
) (tuiAssistContext, error) {
	source, repository, _, base, err := workspace.openRequest(ctx, request)
	if err != nil {
		return tuiAssistContext{}, err
	}
	snapshot, err := operations.Snapshot(ctx, request)
	if err != nil {
		return tuiAssistContext{}, fmt.Errorf("capture deployment snapshot for LLM projection: %w", err)
	}
	projection, err := application.AssistProjectionFor(ctx, request, snapshot)
	if err != nil {
		return tuiAssistContext{}, fmt.Errorf("build LLM deployment projection: %w", err)
	}
	forbidden, err := assistForbiddenValues(ctx, source, request.Service, snapshot)
	if err != nil {
		return tuiAssistContext{}, err
	}
	_, rereadRepository, _, rereadBase, err := workspace.openRequest(ctx, request)
	if err != nil || rereadRepository != repository || rereadBase != base {
		return tuiAssistContext{}, errDeploymentEditInvalid
	}
	stableSnapshot := snapshot
	stableSnapshot.CapturedAt = time.Time{}
	stableSnapshot.DroppedEvents = 0
	// OperationSnapshot is a closed data projection. The duration option makes
	// its only otherwise unsupported standard-library scalar representable.
	snapshotJSON, _ := json.Marshal(
		&stableSnapshot,
		json.Deterministic(true),
		jsonv1.FormatDurationAsNano(true),
	)
	payload := strings.Join([]string{
		base.head, base.tree, request.Source.Repository.Digest.String(), projection.Identity, string(snapshotJSON),
	}, "\x00")

	return tuiAssistContext{
		projection: projection, identity: fmt.Sprintf("%x", sha256.Sum256([]byte(payload))),
		forbidden: forbidden,
	}, nil
}

//nolint:cyclop // This inventory collects each allowlisted private Compose/runtime value before network dispatch.
func assistForbiddenValues(
	ctx context.Context,
	source compose.Source,
	service string,
	snapshot application.OperationSnapshot,
) (map[string][]string, error) {
	project, err := compose.Load(ctx, source)
	if err != nil {
		return nil, errDeploymentEditInvalid
	}
	spec, err := project.ServiceSpec(service)
	if err != nil {
		return nil, errDeploymentEditInvalid
	}
	forbidden := map[string][]string{
		"command": slices.Concat(
			spec.Entrypoint, spec.Command, snapshot.Plan.Image.Entrypoint, snapshot.Plan.Image.Command,
		),
		"runtime ID": {snapshot.Plan.Observation.ID},
	}
	for _, assignments := range [2][]string{spec.Environment, snapshot.Plan.Image.Environment} {
		for _, assignment := range assignments {
			forbidden["environment"] = append(forbidden["environment"], assignment)
			if _, value, found := strings.Cut(assignment, "="); found && value != "" {
				forbidden["environment"] = append(forbidden["environment"], value)
			}
		}
	}
	if spec.Healthcheck != nil {
		forbidden["command"] = append(forbidden["command"], spec.Healthcheck.Test...)
	}
	if snapshot.Plan.Image.Healthcheck != nil {
		forbidden["command"] = append(forbidden["command"], snapshot.Plan.Image.Healthcheck.Test...)
	}
	if source.Repository != nil {
		forbidden["private path"] = append(forbidden["private path"], source.Repository.Root)
	}
	if image, imageErr := project.ImageInput(service); imageErr == nil {
		if registry, found := image.RegistrySource(); found {
			forbidden["image reference"] = append(forbidden["image reference"], registry.String())
		}
	}
	for _, port := range spec.Ports {
		forbidden["port"] = append(forbidden["port"],
			port.HostIP,
			strconv.FormatUint(uint64(port.PublishedPort), 10),
			strconv.FormatUint(uint64(port.TargetPort), 10),
			fmt.Sprintf("%s:%d:%d/%s", port.HostIP, port.PublishedPort, port.TargetPort, port.Protocol),
		)
	}
	for _, mount := range spec.Mounts {
		forbidden["mount"] = append(forbidden["mount"], mount.Source, mount.Target)
	}
	for _, device := range spec.Devices {
		forbidden["device"] = append(forbidden["device"], device.Source, device.Target)
	}
	for _, mount := range snapshot.Plan.Observation.RuntimeMounts {
		forbidden["runtime ID"] = append(forbidden["runtime ID"], mount.Name, mount.Source)
	}

	return forbidden, nil
}
