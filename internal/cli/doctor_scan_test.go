package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestScanBackupRootReadsCompletePublication(t *testing.T) {
	t.Parallel()

	root := privateDoctorBackupRoot(t)
	want := publishDoctorBackup(t, root)
	publications, err := scanBackupRoot(context.Background(), root)
	if err != nil {
		t.Fatalf("scanBackupRoot() error = %v", err)
	}
	if len(publications) != 1 || publications[0].ManifestDigest != want.ManifestDigest ||
		publications[0].Manifest.TransactionID != want.Manifest.TransactionID {
		t.Fatalf("publications = %#v", publications)
	}

	identifier, valid := parseBackupDirectoryName(want.Manifest.TransactionID.String())
	if !valid || identifier != want.Manifest.TransactionID {
		t.Fatalf("parseBackupDirectoryName() = %x, %t", identifier, valid)
	}
}

func TestScanBackupRootRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "ordinary file",
			setup: func(t *testing.T, root string) {
				t.Helper()
				requireDoctorTestNoError(t, os.WriteFile(filepath.Join(root, "entry"), nil, 0o600))
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, root string) {
				t.Helper()
				requireDoctorTestNoError(t, os.Symlink("missing", filepath.Join(root, "entry")))
			},
		},
		{
			name: "invalid directory name",
			setup: func(t *testing.T, root string) {
				t.Helper()
				requireDoctorTestNoError(t, os.Mkdir(filepath.Join(root, "invalid"), 0o700))
			},
		},
		{
			name: "incomplete publication",
			setup: func(t *testing.T, root string) {
				t.Helper()
				identifier := backup.Identifier{4}
				requireDoctorTestNoError(t, os.Mkdir(filepath.Join(root, identifier.String()), 0o700))
			},
		},
		{
			name: "noncanonical directory name",
			setup: func(t *testing.T, root string) {
				t.Helper()
				upper := strings.ToUpper(backup.Identifier{0xab}.String())
				requireDoctorTestNoError(t, os.Mkdir(filepath.Join(root, upper), 0o700))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := privateDoctorBackupRoot(t)
			requireDoctorTestNoError(t, os.Mkdir(root, 0o700))
			test.setup(t, root)
			if _, err := scanBackupRoot(context.Background(), root); !errors.Is(err, errGitOpsRepositoryInvalid) {
				t.Fatalf("scanBackupRoot() error = %v", err)
			}
		})
	}
}

func TestScanBackupRootReportsReadAndCancellationFailures(t *testing.T) {
	t.Parallel()

	fileRoot := privateDoctorBackupRoot(t)
	requireDoctorTestNoError(t, os.WriteFile(fileRoot, nil, 0o600))
	if _, err := scanBackupRoot(context.Background(), fileRoot); err == nil {
		t.Fatal("scanBackupRoot(file) succeeded")
	}

	root := privateDoctorBackupRoot(t)
	requireDoctorTestNoError(t, os.Mkdir(root, 0o700))
	identifier := backup.Identifier{4}
	requireDoctorTestNoError(t, os.Mkdir(filepath.Join(root, identifier.String()), 0o700))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanBackupRoot(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("scanBackupRoot(cancelled) error = %v", err)
	}

	if _, valid := parseBackupDirectoryName("invalid"); valid {
		t.Fatal("parseBackupDirectoryName(invalid) succeeded")
	}
	if _, valid := parseBackupDirectoryName("00"); valid {
		t.Fatal("parseBackupDirectoryName(short) succeeded")
	}
	if _, valid := parseBackupDirectoryName(strings.ToUpper(backup.Identifier{0xab}.String())); valid {
		t.Fatal("parseBackupDirectoryName(uppercase) succeeded")
	}
}

func publishDoctorBackup(t *testing.T, root string) backup.Publication {
	t.Helper()

	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	header := &tar.Header{
		Name: "data", Mode: 0o600, Size: 7, Typeflag: tar.TypeReg, ModTime: time.Unix(1, 0),
	}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if _, err := io.WriteString(writer, "payload"); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	inventory, err := backup.Analyze(
		context.Background(),
		bytes.NewReader(archive.Bytes()),
		int64(archive.Len()),
	)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	manifest := backup.Manifest{
		Version:               1,
		OperationToken:        backup.Identifier{1},
		TransactionID:         backup.Identifier{0xab},
		BaseTransactionID:     backup.Identifier{3},
		Project:               "project",
		Service:               "service",
		Runtime:               domain.RuntimeDocker,
		CreatedUnix:           42,
		SourceDigest:          domain.Hash([]byte("source")),
		EffectiveDigest:       domain.Hash([]byte("effective")),
		ExecutionDigest:       domain.Hash([]byte("execution")),
		PredecessorWorkloadID: "old-workload",
		Artifacts: []backup.Artifact{{
			Mount: domain.RuntimeMount{
				Kind: domain.MountBind, Source: "/private/data", Target: "/data",
			},
			ProvenanceDigest: domain.Hash([]byte("provenance")),
			FileName:         "artifact-000001.tar",
			Inventory:        inventory,
		}},
	}
	capacity, err := backup.PreparePublicationCapacity(root, manifest)
	if err != nil {
		t.Fatalf("PreparePublicationCapacity() error = %v", err)
	}
	publication, err := backup.Publish(context.Background(), root, manifest, []backup.ArchiveInput{{
		Target: "/data", Reader: bytes.NewReader(archive.Bytes()), MaximumBytes: int64(archive.Len()),
	}}, capacity)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	return publication
}

func privateDoctorBackupRoot(t *testing.T) string {
	t.Helper()

	parent := t.TempDir()
	//nolint:gosec // The backup fixture requires a private searchable directory.
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	return filepath.Join(parent, "backups")
}

func requireDoctorTestNoError(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}
