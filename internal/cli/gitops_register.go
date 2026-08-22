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

	// gitOpsRegistration contains only JSON-supported scalar fields.
	raw, _ := json.Marshal(registration) //nolint:errchkjson // This fixed scalar struct cannot fail JSON encoding.
	raw = append(raw, '\n')

	return publishGitOpsRegistration(path, raw, os.WriteFile, os.Rename, os.Remove)
}

func publishGitOpsRegistration(
	path string,
	raw []byte,
	writeFile func(string, []byte, os.FileMode) error,
	rename func(string, string) error,
	remove func(string) error,
) error {
	temporary := path + ".tmp"
	if err := writeFile(temporary, raw, gitOpsRegistrationMode); err != nil {
		return fmt.Errorf("write gitops registration: %w", err)
	}
	if err := rename(temporary, path); err != nil {
		_ = remove(temporary)

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
