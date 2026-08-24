//go:build linux || darwin

package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

var errPublicationTest = errors.New("publication test failure")

type publicationFailureCase struct {
	name      string
	configure func(*publicationOperations, context.CancelFunc)
	archives  func(*testing.T, Manifest) []ArchiveInput
	want      error
}

func TestPublishCleansFailuresBeforePublication(t *testing.T) {
	t.Parallel()

	tests := []publicationFailureCase{
		{name: "mkdir", configure: failMkdir, want: ErrInvalidBackupRoot},
		{name: "open partial", configure: failOpenDirectory, want: ErrInvalidBackupRoot},
		{name: "invalid partial descriptor", configure: invalidOpenDirectory, want: ErrInvalidBackupRoot},
		{name: "open artifact", configure: failOpenFile, want: ErrInvalidBackupRoot},
		{name: "invalid artifact descriptor", configure: invalidOpenFile, want: ErrInvalidBackupRoot},
		{name: "malformed archive", archives: malformedPublicationArchive, want: ErrInvalidArchive},
		{name: "archive sync", configure: failFileSync(1), want: ErrInvalidBackupRoot},
		{name: "archive close", configure: failFileClose(1), want: ErrInvalidBackupRoot},
		{name: "manifest write", configure: failManifestWrite, want: ErrInvalidBackupRoot},
		{name: "manifest short write", configure: shortManifestWrite, want: ErrInvalidBackupRoot},
		{name: "manifest sync", configure: failFileSync(3), want: ErrInvalidBackupRoot},
		{name: "manifest close", configure: failFileClose(3), want: ErrInvalidBackupRoot},
		{name: "cancel after manifest", configure: cancelAfterManifestWrite, want: context.Canceled},
		{name: "cancel after partial sync", configure: cancelAfterDirectorySync, want: context.Canceled},
		{name: "partial directory sync", configure: failDirectorySync(1), want: ErrInvalidBackupRoot},
		{name: "rename", configure: failRename, want: ErrInvalidBackupRoot},
		{name: "missing conflict target", configure: failRenameConflict, want: ErrPublicationConflict},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			manifest := validManifestForTest(t)
			root := privatePublicationRoot(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			operations := defaultPublicationOperations()
			if test.configure != nil {
				test.configure(&operations, cancel)
			}
			archives := publicationArchives(t, manifest, 0)
			if test.archives != nil {
				archives = test.archives(t, manifest)
			}

			_, err := publishWithOperations(
				ctx, root, manifest, archives, strings.NewReader(strings.Repeat("n", partialRandomBytes)), operations,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("publishWithOperations error = %v, want %v", err, test.want)
			}
			assertEmptyDirectory(t, root)
		})
	}
}

func malformedPublicationArchive(t *testing.T, manifest Manifest) []ArchiveInput {
	t.Helper()

	inputs := publicationArchives(t, manifest, 0)
	inputs[0].Reader = strings.NewReader("not a tar archive")
	inputs[0].MaximumBytes = 17

	return inputs
}

func failManifestWrite(operations *publicationOperations, _ context.CancelFunc) {
	base := operations.writeFile
	operations.writeFile = func(file *os.File, value []byte) (int, error) {
		written, err := base(file, value)

		return written, errors.Join(err, errPublicationTest)
	}
}

func shortManifestWrite(operations *publicationOperations, _ context.CancelFunc) {
	base := operations.writeFile
	operations.writeFile = func(file *os.File, value []byte) (int, error) {
		written, err := base(file, value)
		if written > 0 {
			written--
		}

		return written, err
	}
}

func failMkdir(operations *publicationOperations, _ context.CancelFunc) {
	operations.mkdir = func(int, string, uint32) error { return errPublicationTest }
}

func failOpenDirectory(operations *publicationOperations, _ context.CancelFunc) {
	operations.openDirectory = func(int, string, int, uint32) (int, error) {
		return -1, errPublicationTest
	}
}

func invalidOpenDirectory(operations *publicationOperations, _ context.CancelFunc) {
	operations.openDirectory = func(int, string, int, uint32) (int, error) { return -1, nil }
}

func failOpenFile(operations *publicationOperations, _ context.CancelFunc) {
	operations.openFile = func(int, string, int, uint32) (int, error) { return -1, errPublicationTest }
}

func invalidOpenFile(operations *publicationOperations, _ context.CancelFunc) {
	operations.openFile = func(int, string, int, uint32) (int, error) { return -1, nil }
}

func failFileSync(wantCall int) func(*publicationOperations, context.CancelFunc) {
	return func(operations *publicationOperations, _ context.CancelFunc) {
		base := operations.syncFile
		calls := 0
		operations.syncFile = func(file *os.File) error {
			calls++
			err := base(file)
			if calls == wantCall {
				return errors.Join(err, errPublicationTest)
			}

			return err
		}
	}
}

func failFileClose(wantCall int) func(*publicationOperations, context.CancelFunc) {
	return func(operations *publicationOperations, _ context.CancelFunc) {
		base := operations.closeFile
		calls := 0
		operations.closeFile = func(file *os.File) error {
			calls++
			err := base(file)
			if calls == wantCall {
				return errors.Join(err, errPublicationTest)
			}

			return err
		}
	}
}

func cancelAfterManifestWrite(operations *publicationOperations, cancel context.CancelFunc) {
	base := operations.writeFile
	operations.writeFile = func(file *os.File, value []byte) (int, error) {
		written, err := base(file, value)
		cancel()

		return written, err
	}
}

func failDirectorySync(wantCall int) func(*publicationOperations, context.CancelFunc) {
	return func(operations *publicationOperations, _ context.CancelFunc) {
		base := operations.syncDirectory
		calls := 0
		operations.syncDirectory = func(descriptor int) error {
			calls++
			if calls == wantCall {
				return errPublicationTest
			}

			return base(descriptor)
		}
	}
}

func cancelAfterDirectorySync(operations *publicationOperations, cancel context.CancelFunc) {
	base := operations.syncDirectory
	calls := 0
	operations.syncDirectory = func(descriptor int) error {
		calls++
		err := base(descriptor)
		if calls == 1 {
			cancel()
		}

		return err
	}
}

func failRename(operations *publicationOperations, _ context.CancelFunc) {
	operations.rename = func(int, string, string) error { return errPublicationTest }
}

func failRenameConflict(operations *publicationOperations, _ context.CancelFunc) {
	operations.rename = func(int, string, string) error { return unix.EEXIST }
}

func TestPublishReportsUnknownAfterRename(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	root := privatePublicationRoot(t)
	operations := defaultPublicationOperations()
	failDirectorySync(2)(&operations, func() {})

	_, err := publishWithOperations(
		context.Background(), root, manifest, publicationArchives(t, manifest, 0),
		strings.NewReader(strings.Repeat("n", partialRandomBytes)), operations,
	)
	if !errors.Is(err, ErrPublicationUnknown) {
		t.Fatalf("publishWithOperations error = %v", err)
	}
	result, err := publishCapacityChecked(context.Background(), t, root, manifest, publicationArchives(t, manifest, 0))
	if err != nil || result.ManifestDigest == ([32]byte{}) {
		t.Fatalf("probe existing publication = %#v, %v", result, err)
	}
}

func TestExistingPublicationVerificationRejectsDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*testing.T, string, Manifest)
	}{
		{name: "broad directory", mutate: broadenPublicationDirectory},
		{name: "extra entry", mutate: addPublicationEntry},
		{name: "broad manifest", mutate: broadenPublicationManifest},
		{name: "missing artifact", mutate: removePublicationArtifact},
		{name: "changed artifact", mutate: changePublicationArtifact},
		{name: "broad artifact", mutate: broadenPublicationArtifact},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			manifest := validManifestForTest(t)
			root := privatePublicationRoot(t)
			if _, err := publishCapacityChecked(
				context.Background(), t, root, manifest, publicationArchives(t, manifest, 0),
			); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root, manifest)

			_, err := publishCapacityChecked(context.Background(), t, root, manifest, publicationArchives(t, manifest, 0))
			if !errors.Is(err, ErrPublicationConflict) {
				t.Fatalf("drift error = %v", err)
			}
		})
	}
}

func publicationPath(root string, manifest Manifest) string {
	return filepath.Join(root, manifest.TransactionID.String())
}

func broadenPublicationDirectory(t *testing.T, root string, manifest Manifest) {
	t.Helper()

	if err := os.Chmod(publicationPath(root, manifest), 0o755); err != nil { //nolint:gosec // Unsafe mode is the fixture.
		t.Fatal(err)
	}
}

func addPublicationEntry(t *testing.T, root string, manifest Manifest) {
	t.Helper()

	path := filepath.Join(publicationPath(root, manifest), "extra")
	if err := os.WriteFile(path, []byte("extra"), privateFileMode); err != nil {
		t.Fatal(err)
	}
}

func broadenPublicationManifest(t *testing.T, root string, manifest Manifest) {
	t.Helper()

	path := filepath.Join(publicationPath(root, manifest), manifestName)
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // Unsafe mode is the fixture.
		t.Fatal(err)
	}
}

func removePublicationArtifact(t *testing.T, root string, manifest Manifest) {
	t.Helper()

	path := filepath.Join(publicationPath(root, manifest), manifest.Artifacts[0].FileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func changePublicationArtifact(t *testing.T, root string, manifest Manifest) {
	t.Helper()

	path := filepath.Join(publicationPath(root, manifest), manifest.Artifacts[0].FileName)
	value, err := os.ReadFile(path) //nolint:gosec // The path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	value[0] ^= 1
	if err = os.WriteFile(path, value, privateFileMode); err != nil { //nolint:gosec // The path is under t.TempDir.
		t.Fatal(err)
	}
}

func broadenPublicationArtifact(t *testing.T, root string, manifest Manifest) {
	t.Helper()

	path := filepath.Join(publicationPath(root, manifest), manifest.Artifacts[0].FileName)
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // Unsafe mode is the fixture.
		t.Fatal(err)
	}
}

func TestExistingPublicationCancellationIsNotConflict(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	root := privatePublicationRoot(t)
	if _, err := publishCapacityChecked(
		context.Background(), t, root, manifest, publicationArchives(t, manifest, 0),
	); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	operations := defaultPublicationOperations()
	baseRename := operations.rename
	operations.rename = func(directory int, source, destination string) error {
		err := baseRename(directory, source, destination)
		cancel()

		return err
	}
	_, err := publishWithOperations(
		ctx, root, manifest, publicationArchives(t, manifest, 0),
		strings.NewReader(strings.Repeat("n", partialRandomBytes)), operations,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification error = %v", err)
	}
}

func TestCreatePartialRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	operations := defaultPublicationOperations()
	partial, err := createPartial(
		nil, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if partial != nil || !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("createPartial(nil) = %#v, %v", partial, err)
	}

	rootPath := privatePublicationRoot(t)
	root, openErr := openBackupRoot(rootPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer func() { _ = root.close() }()
	partial, err = createPartial(
		root, Identifier{}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if partial != nil || !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("createPartial(zero) = %#v, %v", partial, err)
	}
}

func TestStageRejectsInvalidManifest(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	operations := defaultPublicationOperations()
	partial, err := createPartial(
		root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = partial.stage(context.Background(), Manifest{}, nil); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("stage error = %v", err)
	}
	if err = partial.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestPartialFileHelpers(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	operations := defaultPublicationOperations()
	partial, err := createPartial(
		root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, identity, createErr := partial.createFile(manifestName)
	if createErr != nil {
		t.Fatal(createErr)
	}
	partial.entries = append(partial.entries, publicationEntry{name: manifestName, identity: identity})
	if createErr = file.Close(); createErr != nil {
		t.Fatal(createErr)
	}
	if _, _, createErr := partial.createFile(manifestName); !errors.Is(createErr, ErrInvalidBackupRoot) {
		t.Fatalf("duplicate createFile error = %v", createErr)
	}
	if createErr = partial.writeManifest([]byte("{}")); !errors.Is(createErr, ErrInvalidBackupRoot) {
		t.Fatalf("duplicate writeManifest error = %v", createErr)
	}
	if partial.fileValid(nil, manifestName, identity) {
		t.Fatal("nil file is valid")
	}
	if err = partial.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, _, createErr = partial.createFile("closed"); !errors.Is(createErr, ErrInvalidBackupRoot) {
		t.Fatalf("closed partial createFile error = %v", createErr)
	}
}

func TestPublicationDescriptorAndReaderErrors(t *testing.T) {
	t.Parallel()

	if names := directoryNames(openRegularFile(t)); names != nil {
		t.Fatalf("regular file directory names = %q", names)
	}
	if err := (&directoryAnchor{descriptor: 1 << 30}).close(); err == nil {
		t.Fatal("invalid directory descriptor closed without error")
	}
	if err := (&partialPublication{descriptor: 1 << 30}).close(); err == nil {
		t.Fatal("invalid partial descriptor closed without error")
	}

	reader := &contextReader{cancelled: context.Background().Err, source: errorReader{}}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, errPublicationTest) {
		t.Fatalf("contextReader source error = %v", err)
	}
}

func TestCreatePartialRejectsChangedCreationIdentity(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	operations := defaultPublicationOperations()
	baseMkdir := operations.mkdir
	operations.mkdir = func(directory int, name string, mode uint32) error {
		if mkdirErr := baseMkdir(directory, name, mode); mkdirErr != nil {
			return mkdirErr
		}

		return unix.Fchmodat(directory, name, 0o755, 0)
	}
	partial, err := createPartial(
		root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if partial != nil || !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("createPartial = %#v, %v", partial, err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil || len(entries) != 1 {
		t.Fatalf("partial entries = %#v, %v", entries, err)
	}
	path := filepath.Join(rootPath, entries[0].Name())
	if err = os.Chmod(path, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func openRegularFile(t *testing.T) int {
	t.Helper()

	path := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(path, nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path) //nolint:gosec // The path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	return int(file.Fd())
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errPublicationTest
}

func TestPublicationCleanupRejectsReplacement(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	operations := defaultPublicationOperations()
	partial, err := createPartial(
		root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, identity, err := partial.createFile(manifestName)
	if err != nil {
		t.Fatal(err)
	}
	partial.entries = append(partial.entries, publicationEntry{name: manifestName, identity: identity})
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(rootPath, partial.name, manifestName)); err != nil {
		t.Fatal(err)
	}
	if err = partial.cleanup(); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("replacement cleanup error = %v", err)
	}

	if err = os.Remove(filepath.Join(rootPath, partial.name)); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationCleanupFilesystemFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*publicationOperations)
	}{
		{name: "partial sync", configure: func(operations *publicationOperations) {
			operations.syncDirectory = func(int) error { return errPublicationTest }
		}},
		{name: "directory unlink", configure: func(operations *publicationOperations) {
			base := operations.unlink
			operations.unlink = func(directory int, name string, flags int) error {
				if flags == unix.AT_REMOVEDIR {
					return errPublicationTest
				}

				return base(directory, name, flags)
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rootPath := privatePublicationRoot(t)
			root, err := openBackupRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.close() }()
			operations := defaultPublicationOperations()
			test.configure(&operations)
			partial, err := createPartial(
				root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
			)
			if err != nil {
				t.Fatal(err)
			}
			partialName := partial.name
			if err = partial.cleanup(); !errors.Is(err, ErrInvalidBackupRoot) {
				t.Fatalf("cleanup error = %v", err)
			}
			if err = os.Remove(filepath.Join(rootPath, partialName)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRemoveCreatedFile(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	operations := defaultPublicationOperations()
	partial, err := createPartial(
		root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, identity, err := partial.createFile("owned")
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if !partial.removeCreatedFile("owned", identity) {
		t.Fatal("owned file was not removed")
	}
	if err = partial.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveCreatedDirectoryRejectsDriftAndUnlinkFailure(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	const name = "partial"
	if err = unix.Mkdirat(root.descriptor, name, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	identity, valid := entryIdentity(root.descriptor, name)
	if !valid {
		t.Fatal("created directory has no identity")
	}

	drifted := identity
	drifted.inode++
	operations := defaultPublicationOperations()
	if err = removeCreatedDirectory(root, name, drifted, operations); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("drifted removal error = %v", err)
	}
	baseUnlink := operations.unlink
	operations.unlink = func(int, string, int) error { return errPublicationTest }
	if err = removeCreatedDirectory(root, name, identity, operations); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("failed unlink error = %v", err)
	}
	operations.unlink = baseUnlink
	if err = removeCreatedDirectory(root, name, identity, operations); err != nil {
		t.Fatal(err)
	}
}

func TestCreateFileRejectsHardLink(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	operations := defaultPublicationOperations()
	baseOpen := operations.openFile
	operations.openFile = func(directory int, name string, flags int, mode uint32) (int, error) {
		descriptor, openErr := baseOpen(directory, name, flags, mode)
		if openErr != nil {
			return descriptor, openErr
		}
		if linkErr := unix.Linkat(directory, name, directory, name+".link", 0); linkErr != nil {
			_ = unix.Close(descriptor)

			return -1, fmt.Errorf("create hard-link fixture: %w", linkErr)
		}

		return descriptor, nil
	}
	partial, err := createPartial(
		root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = partial.createFile("linked"); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("hard-linked createFile error = %v", err)
	}
	for _, name := range []string{"linked", "linked.link"} {
		if err = unix.Unlinkat(partial.descriptor, name, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err = partial.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRejectsInvalidPartialDescriptor(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	operations := defaultPublicationOperations()
	partial, err := createPartial(
		root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), operations,
	)
	if err != nil {
		t.Fatal(err)
	}
	partialName := partial.name
	if err = unix.Close(partial.descriptor); err != nil {
		t.Fatal(err)
	}
	partial.descriptor = 1 << 30
	if err = partial.cleanup(); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("invalid partial cleanup = %v", err)
	}
	if err = os.Remove(filepath.Join(rootPath, partialName)); err != nil {
		t.Fatal(err)
	}
}

func TestRenameNoReplaceRejectsInvalidDirectory(t *testing.T) {
	t.Parallel()

	if err := renameNoReplace(-1, "source", "destination"); err == nil || !errors.Is(err, unix.EBADF) {
		t.Fatalf("renameNoReplace error = %v", err)
	}
}

func TestOpenDirectoryReportsFilesystemRootFailure(t *testing.T) {
	t.Parallel()

	_, err := openDirectoryWith("/", func(string, int, uint32) (int, error) {
		return -1, errPublicationTest
	})
	if !errors.Is(err, errPublicationTest) {
		t.Fatalf("openDirectoryWith error = %v", err)
	}
}

func TestOpenBackupRootRejectsCreationFailure(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil { //nolint:gosec // The restrictive mode forces mkdir failure.
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(parent, privateDirectoryMode) }()

	_, err := openBackupRoot(filepath.Join(parent, "backups"))
	if unix.Geteuid() != 0 && !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("openBackupRoot error = %v", err)
	}
}

func TestOpenBackupRootRejectsParentSyncFailure(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRootWithSync(rootPath, func(int) error { return errPublicationTest })
	if root != nil || !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("openBackupRootWithSync = %#v, %v", root, err)
	}
}

func TestReadPrivateFileLimitAndCloseFailure(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	if err = os.WriteFile(filepath.Join(rootPath, "value"), []byte("long"), privateFileMode); err != nil {
		t.Fatal(err)
	}

	operations := defaultPublicationOperations()
	value, valid := readPrivateFile(context.Background(), root.descriptor, "value", 3, operations)
	if valid || !bytes.Equal(value, []byte("long")) {
		t.Fatalf("bounded read = %q, %t", value, valid)
	}
	baseClose := operations.closeFile
	operations.closeFile = func(file *os.File) error {
		return errors.Join(baseClose(file), errPublicationTest)
	}
	if _, valid := readPrivateFile(context.Background(), root.descriptor, "value", 4, operations); valid {
		t.Fatal("read with close failure succeeded")
	}
	if verifyArtifactFile(
		context.Background(), root.descriptor, Artifact{FileName: "missing"}, operations,
	) {
		t.Fatal("missing artifact verified")
	}
}
