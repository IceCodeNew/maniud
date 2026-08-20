package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestExecuteDoctorReportsEmptyBackupRoot(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	output := new(bytes.Buffer)
	err := executeDoctor(context.Background(), doctorInvocation{
		reindexBackups: true,
	}, output, map[string]string{homeKey: home})
	if err != nil {
		t.Fatalf("executeDoctor() error = %v", err)
	}

	var report doctorReport
	if err = json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if report.Replaced || len(report.Entries) != 0 {
		t.Fatalf("report = %#v", report)
	}
	if filepath.Base(filepath.Dir(report.Root)) != "maniud" {
		t.Fatalf("root = %q", report.Root)
	}
	statePath, err := defaultStatePath(map[string]string{homeKey: home})
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	if _, statErr := os.Stat(statePath); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run state error = %v", statErr)
	}
}

func TestExecuteDoctorConfirmsEmptyBackupIndex(t *testing.T) {
	t.Parallel()

	statePath := privateDoctorStatePath(t)
	output := new(bytes.Buffer)
	err := executeDoctor(context.Background(), doctorInvocation{
		reindexBackups: true,
		confirm:        true,
		state:          statePath,
	}, output, nil)
	if err != nil {
		t.Fatalf("executeDoctor() error = %v", err)
	}

	var report doctorReport
	if err = json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !report.Replaced || len(report.Entries) != 0 {
		t.Fatalf("report = %#v", report)
	}
	if _, err = os.Stat(statePath); err != nil {
		t.Fatalf("state error = %v", err)
	}
}

func TestExecuteDoctorRejectsRelativeState(t *testing.T) {
	t.Parallel()

	err := executeDoctor(context.Background(), doctorInvocation{
		reindexBackups: true,
		state:          "relative.db",
	}, new(bytes.Buffer), map[string]string{homeKey: t.TempDir()})
	if err == nil {
		t.Fatal("executeDoctor(relative state) succeeded")
	}
}

func TestExecuteDoctorRejectsMissingOperation(t *testing.T) {
	t.Parallel()

	err := executeDoctorWithDependencies(
		context.Background(),
		doctorInvocation{},
		new(bytes.Buffer),
		nil,
		defaultDoctorDependencies(),
	)
	if !errors.Is(err, errInvalidArguments) {
		t.Fatalf("executeDoctorWithDependencies() error = %v", err)
	}
}

func TestWriteDoctorReportPropagatesOutputFailure(t *testing.T) {
	t.Parallel()

	err := writeDoctorReport(failingWriter{}, "root", nil, false)
	if !errors.Is(err, errClosedOutput) {
		t.Fatalf("writeDoctorReport() error = %v", err)
	}
}

func TestClassifyDoctorCommandFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code domain.ErrorCode
	}{
		{name: "context canceled", err: context.Canceled, code: domain.ErrorOperationCancelled},
		{name: "invalid", err: errInvalidArguments, code: domain.ErrorInvalidInput},
		{name: "missing state home", err: errStateHomeUnavailable, code: domain.ErrorInvalidInput},
		{name: "invalid state home", err: errStateHomeInvalid, code: domain.ErrorInvalidInput},
		{name: "invalid GitOps repository", err: errGitOpsRepositoryInvalid, code: domain.ErrorInvalidInput},
		{name: "apply", err: errDoctorTest, code: domain.ErrorApplyFailed},
	}

	if classifyDoctorCommandFailure(nil) != nil {
		t.Fatal("classifyDoctorCommandFailure(nil) returned a failure")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			failure := classifyDoctorCommandFailure(test.err)
			if failure == nil || failure.Code() != test.code {
				t.Fatalf("classifyDoctorCommandFailure() = %#v", failure)
			}
		})
	}
}

func TestRunDoctorRequiresExecutor(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	status := runDoctor(invocation{
		arguments: doctorInvocation{reindexBackups: true},
	}, output, nil, nil)
	if status != 1 || output.String() != internalErrorJSON {
		t.Fatalf("runDoctor() = %d, %q", status, output.String())
	}
}

func privateDoctorStatePath(t *testing.T) string {
	t.Helper()

	directory := t.TempDir()
	//nolint:gosec // The state fixture requires a private searchable directory.
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}

	return filepath.Join(directory, "maniud", stateDatabaseName)
}
