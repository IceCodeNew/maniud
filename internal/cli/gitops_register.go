package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	gitOpsRegistrationName      = "gitops.json"
	gitOpsRegistrationMode      = os.FileMode(0o600)
	gitOpsRegistrationVersion   = 1
	maximumGitOpsRegistration   = 4 << 10
	gitOpsRegistrationDirMode   = os.FileMode(0o700)
	gitOpsRemoteName            = "origin"
	errGitOpsRepositoryInvalid  = gitOpsRepositoryError("gitops repository is invalid")
	errGitOpsRegistrationExists = gitOpsRepositoryError("gitops registration already exists")
	errAlreadyRegisteredSame    = gitOpsRepositoryError("gitops registration already matches")
)

type gitOpsRepositoryError string

func (err gitOpsRepositoryError) Error() string {
	return string(err)
}

type gitOpsRegistration struct {
	Version    int    `json:"version"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Remote     string `json:"remote"`
	Commit     string `json:"commit"`
}

func gitOpsRegistrationPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), gitOpsRegistrationName)
}

func writeGitOpsRegistration(path string, registration gitOpsRegistration) error {
	if !validGitOpsRegistration(registration) || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errGitOpsRepositoryInvalid
	}
	if err := prepareGitOpsRegistration(path, registration); err != nil {
		if errors.Is(err, errAlreadyRegisteredSame) {
			return nil
		}

		return err
	}

	raw, err := encodeGitOpsRegistration(registration)
	if err != nil {
		return err
	}

	temporary := path + ".tmp"
	if err = os.WriteFile(temporary, raw, gitOpsRegistrationMode); err != nil {
		return fmt.Errorf("write gitops registration: %w", err)
	}
	if err = os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)

		return fmt.Errorf("publish gitops registration: %w", err)
	}

	return nil
}

func prepareGitOpsRegistration(path string, registration gitOpsRegistration) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, gitOpsRegistrationDirMode); err != nil {
		return fmt.Errorf("create gitops registration directory: %w", err)
	}

	return reuseGitOpsRegistration(path, registration)
}

func reuseGitOpsRegistration(path string, registration gitOpsRegistration) error {
	existing, err := readGitOpsRegistration(path)
	switch {
	case err == nil && existing == registration:
		return errAlreadyRegisteredSame
	case err == nil:
		return errGitOpsRegistrationExists
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		if _, statErr := os.Lstat(path); statErr == nil {
			return errGitOpsRegistrationExists
		}

		return nil
	}
}

func readGitOpsRegistration(path string) (gitOpsRegistration, error) {
	var empty gitOpsRegistration
	file, err := os.Open(path) //nolint:gosec // Path is an absolute, caller-validated private state file.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, fmt.Errorf("open gitops registration: %w", err)
		}

		return empty, fmt.Errorf("open gitops registration: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != gitOpsRegistrationMode {
		return empty, errGitOpsRepositoryInvalid
	}

	var registration gitOpsRegistration
	if !jsonstrict.Decode(file, maximumGitOpsRegistration, &registration) || !validGitOpsRegistration(registration) {
		return empty, errGitOpsRepositoryInvalid
	}

	return registration, nil
}

func encodeGitOpsRegistration(registration gitOpsRegistration) ([]byte, error) {
	if !validGitOpsRegistration(registration) {
		return nil, errGitOpsRepositoryInvalid
	}

	raw, err := json.Marshal(registration)
	if err != nil {
		return nil, fmt.Errorf("encode gitops registration: %w", err)
	}

	return append(raw, '\n'), nil
}

func validGitOpsRegistration(registration gitOpsRegistration) bool {
	return registration.Version == gitOpsRegistrationVersion &&
		filepath.IsAbs(registration.Repository) &&
		filepath.Clean(registration.Repository) == registration.Repository &&
		validGitOpsBranch(registration.Branch) &&
		registration.Remote == gitOpsRemoteName &&
		validGitObjectID(registration.Commit)
}

func validGitOpsBranch(value string) bool {
	return value != "" && !strings.ContainsAny(value, " \t\n\\:~^?*[") &&
		!strings.HasPrefix(value, "-") && !strings.HasPrefix(value, ".") &&
		!strings.Contains(value, "..") && !strings.HasSuffix(value, ".lock")
}
