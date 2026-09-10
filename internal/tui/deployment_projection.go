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
	if preview.NoChanges {
		if preview.ComposePath != "" || len(preview.Changes) != 0 || preview.Restore != "" || preview.Diff != "" {
			return DeploymentEditPreview{}, errInvalidInput
		}

		return preview, nil
	}
	path, valid := canonicalSourceDiagnosticFile(preview.ComposePath)
	if !valid || len(preview.Changes) > len(application.DeploymentFields()) ||
		len(preview.Changes) == 0 && preview.Restore == "" {
		return DeploymentEditPreview{}, errInvalidInput
	}
	preview.ComposePath = path
	fields := application.DeploymentFields()
	changes := make([]DeploymentFieldChange, len(preview.Changes))
	previousIndex := -1
	for index, change := range preview.Changes {
		field, err := application.ParseDeploymentField(change.FieldID)
		fieldIndex := slices.Index(fields, field)
		if err != nil || field.ID() != change.FieldID || fieldIndex <= previousIndex {
			return DeploymentEditPreview{}, errInvalidInput
		}
		change.CurrentValue, err = canonicalDeploymentChangeValue(
			change.FieldID, change.CurrentValue, change.CurrentPresent,
		)
		if err != nil {
			return DeploymentEditPreview{}, errInvalidInput
		}
		change.ProposedValue, err = canonicalDeploymentChangeValue(
			change.FieldID, change.ProposedValue, change.ProposedPresent,
		)
		if err != nil || change.CurrentPresent == change.ProposedPresent &&
			change.CurrentValue == change.ProposedValue {
			return DeploymentEditPreview{}, errInvalidInput
		}
		changes[index] = change
		previousIndex = fieldIndex
	}
	if preview.Restore != "" && !validRevision(preview.Restore) {
		return DeploymentEditPreview{}, errInvalidInput
	}
	diff, err := canonicalStagedDiff(preview.Diff)
	if err != nil {
		return DeploymentEditPreview{}, err
	}
	preview.Changes = changes
	preview.Diff = diff

	return preview, nil
}

func canonicalStagedDiff(value string) (string, error) {
	diff, err := terminaltext.Canonicalize(value, terminaltext.Limits{
		Bytes:     maximumStagedDiffBytes,
		Runes:     maximumStagedDiffBytes,
		Lines:     maximumStagedDiffLines,
		LineCells: maximumDisplayCells,
	})
	if err != nil || diff == "" {
		return "", errors.Join(errInvalidInput, err)
	}

	return diff, nil
}

func canonicalDeploymentChangeValue(fieldID, value string, present bool) (string, error) {
	canonical, err := canonicalDisplay(value)
	if err != nil || !present && canonical != "" || present && canonical == "" {
		return "", errors.Join(errInvalidInput, err)
	}
	if present {
		if _, err = application.ParseDeploymentPatch(fieldID, canonical, false); err != nil {
			return "", errInvalidInput
		}
	}

	return canonical, nil
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
