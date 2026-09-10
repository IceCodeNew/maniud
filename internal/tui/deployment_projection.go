package tui

import (
	"errors"
	"slices"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

func canonicalDeploymentFields(fields []DeploymentFieldState) ([]DeploymentFieldState, error) {
	expected := application.DeploymentFields()
	if len(fields) != len(expected) {
		return nil, errInvalidInput
	}
	result := make([]DeploymentFieldState, len(fields))
	for index, field := range fields {
		parsed, err := application.ParseDeploymentField(field.ID)
		if err != nil || parsed.ID() != field.ID || parsed != expected[index] {
			return nil, errInvalidInput
		}
		field.Value, err = canonicalDisplay(field.Value)
		if err != nil || field.AllowsUnset != parsed.AllowsUnset() {
			return nil, errors.Join(errInvalidInput, err)
		}
		result[index] = field
	}

	return result, nil
}

//nolint:cyclop // The external preview projection is validated as one fail-closed contract.
func canonicalDeploymentPreview(preview DeploymentEditPreview) (DeploymentEditPreview, error) {
	path, valid := canonicalSourceDiagnosticFile(preview.ComposePath)
	if !valid || len(preview.FieldIDs) == 0 && preview.Restore == "" ||
		len(preview.FieldIDs) != 0 && preview.Restore != "" {
		return DeploymentEditPreview{}, errInvalidInput
	}
	preview.ComposePath = path
	seen := make(map[string]struct{}, len(preview.FieldIDs))
	for _, fieldID := range preview.FieldIDs {
		field, err := application.ParseDeploymentField(fieldID)
		if err != nil || field.ID() != fieldID {
			return DeploymentEditPreview{}, errInvalidInput
		}
		if _, duplicate := seen[fieldID]; duplicate {
			return DeploymentEditPreview{}, errInvalidInput
		}
		seen[fieldID] = struct{}{}
	}
	if preview.Restore != "" && !validRevision(preview.Restore) {
		return DeploymentEditPreview{}, errInvalidInput
	}

	return preview, nil
}

func canonicalStagedDeployment(staged StagedDeploymentEdit) (StagedDeploymentEdit, error) {
	canonical, err := canonicalStagedService(StagedService{
		Diff: staged.Diff, ComposePath: staged.ComposePath, CommitMessage: staged.CommitMessage,
	})
	if err != nil {
		return StagedDeploymentEdit{}, err
	}

	return StagedDeploymentEdit{
		Diff: canonical.Diff, ComposePath: canonical.ComposePath, CommitMessage: canonical.CommitMessage,
	}, nil
}

func canonicalDeploymentHistory(
	history []DeploymentHistoryEntry,
) ([]DeploymentHistoryEntry, error) {
	if len(history) == 0 || len(history) > 100 {
		return nil, errInvalidInput
	}
	result := make([]DeploymentHistoryEntry, len(history))
	seen := make(map[string]struct{}, len(history))
	for index, entry := range history {
		if !validRevision(entry.Revision) {
			return nil, errInvalidInput
		}
		if _, duplicate := seen[entry.Revision]; duplicate {
			return nil, errInvalidInput
		}
		seen[entry.Revision] = struct{}{}
		subject, err := terminaltext.Canonicalize(entry.Subject, displayLimits())
		if err != nil || subject == "" || strings.ContainsRune(subject, '\n') {
			return nil, errors.Join(errInvalidInput, err)
		}
		entry.Subject = subject
		result[index] = entry
	}

	return slices.Clip(result), nil
}

func validRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}

	return true
}
