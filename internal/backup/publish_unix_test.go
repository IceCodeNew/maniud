//go:build linux || darwin

package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"golang.org/x/sys/unix"
)

type shortArchiveWriter struct{}

func (shortArchiveWriter) Write(value []byte) (int, error) {
	return len(value) - 1, nil
}

type failedArchiveWriter struct{}

func (failedArchiveWriter) Write([]byte) (int, error) {
	return 0, errArchiveReadTest
}

func publicationArchives(t *testing.T, manifest Manifest, padding int) []ArchiveInput {
	t.Helper()

	archive := makeTar(t, regular("data", "payload"))
	archive = append(archive, make([]byte, padding)...)
	inputs := make([]ArchiveInput, len(manifest.Artifacts))
	for index, artifact := range manifest.Artifacts {
		inputs[index] = ArchiveInput{
			Target: artifact.Mount.Target, Reader: bytes.NewReader(archive), MaximumBytes: int64(len(archive)),
		}
	}
	slices.Reverse(inputs)

	return inputs
}

func TestArchiveLimitWriterStopsBeforeReservedSize(t *testing.T) {
	t.Parallel()

	var destination bytes.Buffer
	writer := archiveLimitWriter{destination: &destination, remaining: 2}
	if written, err := writer.Write([]byte("abc")); written != 2 || !errors.Is(err, ErrArchiveLimit) ||
		destination.String() != "ab" {
		t.Fatalf("bounded write = %d, %v, %q", written, err, destination.String())
	}
	if written, err := writer.Write([]byte("x")); written != 0 || !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("exhausted write = %d, %v", written, err)
	}
	exact := archiveLimitWriter{destination: io.Discard, remaining: 2}
	if written, err := exact.Write([]byte("ab")); written != 2 || err != nil {
		t.Fatalf("exact write = %d, %v", written, err)
	}
	short := archiveLimitWriter{destination: shortArchiveWriter{}, remaining: 2}
	if _, err := short.Write([]byte("ab")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
	failed := archiveLimitWriter{destination: failedArchiveWriter{}, remaining: 2}
	if _, err := failed.Write([]byte("ab")); !errors.Is(err, errArchiveReadTest) {
		t.Fatalf("failed write error = %v", err)
	}
}

func publishCapacityChecked(
	ctx context.Context,
	t *testing.T,
	root string,
	manifest Manifest,
	archives []ArchiveInput,
) (Publication, error) {
	t.Helper()

	capacity, err := PreparePublicationCapacity(root, manifest)
	if err != nil {
		return Publication{}, err
	}

	return Publish(ctx, root, manifest, archives, capacity)
}

func privatePublicationRoot(t *testing.T) string {
	t.Helper()

	parent := t.TempDir()
	if err := os.Chmod(parent, privateDirectoryMode); err != nil {
		t.Fatalf("chmod temporary parent: %v", err)
	}

	return filepath.Join(parent, "backups")
}

func TestOpenReadsPublishedBackupAndReportsAbsence(t *testing.T) {
	t.Parallel()

	root := privatePublicationRoot(t)
	_, found, err := Open(context.Background(), root, Identifier{2})
	if err != nil || found {
		t.Fatalf("Open(missing root) = %t, %v", found, err)
	}

	manifest := validManifestForTest(t)
	published, err := publishCapacityChecked(context.Background(), t, root, manifest, publicationArchives(t, manifest, 0))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got, found, err := Open(context.Background(), root, manifest.TransactionID)
	if err != nil || !found || got.ManifestDigest != published.ManifestDigest {
		t.Fatalf("Open(published) = %#v, %t, %v", got, found, err)
	}

	_, found, err = Open(context.Background(), root, Identifier{9})
	if err != nil || found {
		t.Fatalf("Open(missing publication) = %t, %v", found, err)
	}
	if _, _, err = Open(context.Background(), root, Identifier{}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Open(empty id) = %v", err)
	}
}

func TestPublishCreatesManifestLastAtomicDirectory(t *testing.T) {
	t.Parallel()

	root := privatePublicationRoot(t)
	manifest := validManifestForTest(t)
	result, err := publishCapacityChecked(context.Background(), t, root, manifest, publicationArchives(t, manifest, 0))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	assertPublicationResult(t, manifest, result)
	assertPublishedFiles(t, root, manifest)
	assertPublishedManifest(t, root, manifest, result)
	assertPublicationReplay(t, root, manifest, result)
}

func assertPublicationResult(t *testing.T, manifest Manifest, result Publication) {
	t.Helper()

	wantPath := manifest.TransactionID.String() + "/" + manifestName
	if result.ManifestPath != wantPath || result.ManifestDigest == (domain.Digest{}) ||
		result.Manifest.Artifacts[0].Inventory.ArchiveDigest != manifest.Artifacts[0].Inventory.ArchiveDigest {
		t.Fatalf("publication = %#v", result)
	}
}

func assertPublishedFiles(t *testing.T, root string, manifest Manifest) {
	t.Helper()

	transactionPath := filepath.Join(root, manifest.TransactionID.String())
	names, err := os.ReadDir(transactionPath)
	if err != nil {
		t.Fatalf("ReadDir publication: %v", err)
	}
	if len(names) != len(manifest.Artifacts)+1 {
		t.Fatalf("publication entries = %d", len(names))
	}
	for _, entry := range names {
		info, infoErr := entry.Info()
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if entry.IsDir() || info.Mode().Perm() != privateFileMode {
			t.Fatalf("unsafe publication entry %q: %v", entry.Name(), info.Mode())
		}
	}
}

func assertPublishedManifest(t *testing.T, root string, manifest Manifest, result Publication) {
	t.Helper()

	transactionPath := filepath.Join(root, manifest.TransactionID.String())
	raw, err := os.ReadFile(filepath.Join(transactionPath, manifestName)) //nolint:gosec // Rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	decoded, digest, err := DecodeManifest(bytes.NewReader(raw))
	if err != nil || digest != result.ManifestDigest {
		t.Fatalf("DecodeManifest = %#v, %s, %v", decoded, digest, err)
	}
}

func assertPublicationReplay(t *testing.T, root string, manifest Manifest, result Publication) {
	t.Helper()

	// A retry with identical stopped streams proves the existing complete object
	// and removes its new private partial directory.
	replayed, err := publishCapacityChecked(context.Background(), t, root, manifest, publicationArchives(t, manifest, 0))
	if err != nil || replayed.ManifestDigest != result.ManifestDigest {
		t.Fatalf("idempotent Publish = %#v, %v", replayed, err)
	}
	rootEntries, err := os.ReadDir(root)
	if err != nil || len(rootEntries) != 1 || rootEntries[0].Name() != manifest.TransactionID.String() {
		t.Fatalf("backup root entries = %#v, %v", rootEntries, err)
	}
}

func TestPublishRejectsDriftAndExistingConflict(t *testing.T) {
	t.Parallel()

	root := privatePublicationRoot(t)
	manifest := validManifestForTest(t)
	changed := makeTar(t, regular("data", "changed"))
	inputs := publicationArchives(t, manifest, 0)
	inputs[0].Reader = bytes.NewReader(changed)
	inputs[0].MaximumBytes = int64(len(changed))
	if _, err := publishCapacityChecked(
		context.Background(), t, root, manifest, inputs,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("drift error = %v", err)
	}
	assertEmptyDirectory(t, root)

	first, err := publishCapacityChecked(context.Background(), t, root, manifest, publicationArchives(t, manifest, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = publishCapacityChecked(context.Background(), t, root, manifest, publicationArchives(t, manifest, 512))
	if !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("oversized semantic-equivalent artifact error = %v", err)
	}
	if first.ManifestDigest == (domain.Digest{}) {
		t.Fatal("first publication has no digest")
	}
}

func TestPublishRejectsInvalidRequestsAndCancellation(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	valid := publicationArchives(t, manifest, 0)
	tests := []struct {
		name     string
		manifest Manifest
		inputs   []ArchiveInput
		random   io.Reader
	}{
		{
			name: "invalid manifest", manifest: Manifest{}, inputs: valid,
			random: strings.NewReader(strings.Repeat("x", 16)),
		},
		{name: "nil random", manifest: manifest, inputs: valid, random: nil},
		{
			name: "missing input", manifest: manifest, inputs: valid[:1],
			random: strings.NewReader(strings.Repeat("x", 16)),
		},
		{
			name: "nil reader", manifest: manifest,
			inputs: []ArchiveInput{valid[0], {Target: valid[1].Target, MaximumBytes: 1}},
			random: strings.NewReader(strings.Repeat("x", 16)),
		},
		{
			name: "zero bound", manifest: manifest,
			inputs: []ArchiveInput{
				valid[0], {Target: valid[1].Target, Reader: strings.NewReader("x")},
			},
			random: strings.NewReader(strings.Repeat("x", 16)),
		},
		{
			name: "wrong target", manifest: manifest,
			inputs: []ArchiveInput{
				valid[0], {Target: "/wrong", Reader: strings.NewReader("x"), MaximumBytes: 1},
			},
			random: strings.NewReader(strings.Repeat("x", 16)),
		},
		{
			name: "duplicate target", manifest: manifest, inputs: []ArchiveInput{valid[0], valid[0]},
			random: strings.NewReader(strings.Repeat("x", 16)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := publish(context.Background(), privatePublicationRoot(t), test.manifest, test.inputs, test.random)
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("publish error = %v", err)
			}
		})
	}
}

func TestPublishCancellationAndRandomFailure(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	root := privatePublicationRoot(t)
	_, err := publishCapacityChecked(cancelled, t, root, manifest, publicationArchives(t, manifest, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Publish error = %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled Publish created root: %v", err)
	}

	root = privatePublicationRoot(t)
	_, err = publish(
		context.Background(), root, manifest, publicationArchives(t, manifest, 0), strings.NewReader("short"),
	)
	if !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("short random error = %v", err)
	}
	assertEmptyDirectory(t, root)
}

func TestPublishRejectsUnsafeRoots(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(parent, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	broad := filepath.Join(parent, "broad")
	if err := os.Mkdir(broad, 0o755); err != nil { //nolint:gosec // Unsafe mode is the fixture.
		t.Fatal(err)
	}
	if err := os.Chmod(broad, 0o755); err != nil { //nolint:gosec // Unsafe mode is the fixture.
		t.Fatal(err)
	}

	for _, root := range []string{"", "relative", "/", filepath.Join(parent, "missing", "backups"), symlink, broad} {
		_, err := publishCapacityChecked(context.Background(), t, root, manifest, publicationArchives(t, manifest, 0))
		if !errors.Is(err, ErrInvalidBackupRoot) {
			t.Errorf("Publish(root=%q) error = %v", root, err)
		}
	}
}

func TestPublicationFilesystemHelpers(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })

	partial, err := createPartial(
		root, Identifier{1}, strings.NewReader(strings.Repeat("n", 16)), defaultPublicationOperations(),
	)
	if err != nil {
		t.Fatal(err)
	}
	archive := makeTar(t, regular("data", "payload"))
	inventory, err := partial.writeArchive(context.Background(), artifactName(0), ArchiveInput{
		Target: "/data", Reader: bytes.NewReader(archive), MaximumBytes: int64(len(archive)),
	}, int64(len(archive)))
	if err != nil || inventory.ArchiveDigest == (domain.Digest{}) {
		t.Fatalf("writeArchive = %#v, %v", inventory, err)
	}
	if err := partial.writeManifest([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := partial.cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	assertEmptyDirectory(t, rootPath)
	assertFilesystemIdentityHelpers(t, rootPath, root)
	assertNilPublicationHelpers(t)
}

func assertFilesystemIdentityHelpers(t *testing.T, rootPath string, root *directoryAnchor) {
	t.Helper()

	if !slices.Equal(splitAbsolutePath("/"), []string(nil)) ||
		!slices.Equal(splitAbsolutePath("/one/two"), []string{"one", "two"}) {
		t.Fatal("absolute path splitting changed")
	}
	if _, valid := descriptorIdentity(-1); valid {
		t.Fatal("invalid descriptor has identity")
	}
	if _, valid := pathIdentity(filepath.Join(rootPath, "missing")); valid {
		t.Fatal("missing path has identity")
	}
	if _, valid := entryIdentity(-1, "missing"); valid {
		t.Fatal("invalid directory entry has identity")
	}
	if names := directoryNames(-1); names != nil {
		t.Fatalf("directoryNames(-1) = %#v", names)
	}
	if rootEntryMatches(root, "missing", fileIdentity{}) {
		t.Fatal("missing root entry matched")
	}
}

func assertNilPublicationHelpers(t *testing.T) {
	t.Helper()

	var nilAnchor *directoryAnchor
	if nilAnchor.valid() || nilAnchor.close() != nil {
		t.Fatal("nil directory anchor is valid")
	}
	var nilPartial *partialPublication
	if nilPartial.valid() || nilPartial.cleanup() != nil || nilPartial.close() != nil {
		t.Fatal("nil partial publication is valid")
	}
}

func TestPublicationReadersRejectUnsafeEntriesAndCancellation(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()

	if err := os.WriteFile(filepath.Join(rootPath, "broad"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(rootPath, "broad"), 0o644); err != nil { //nolint:gosec // Unsafe fixture.
		t.Fatal(err)
	}
	operations := defaultPublicationOperations()
	if value, valid := readPrivateFile(
		context.Background(), root.descriptor, "broad", 8, operations,
	); valid || value != nil {
		t.Fatalf("readPrivateFile(broad) = %q, %t", value, valid)
	}
	if value, valid := readPrivateFile(
		context.Background(), root.descriptor, "missing", 8, operations,
	); valid || value != nil {
		t.Fatalf("readPrivateFile(missing) = %q, %t", value, valid)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &contextReader{cancelled: cancelled.Err, source: strings.NewReader("x")}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("contextReader error = %v", err)
	}
}

func assertEmptyDirectory(t *testing.T, path string) {
	t.Helper()

	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", path, err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %s contains %#v", path, entries)
	}
}

func TestRenameNoReplacePreservesExistingDirectory(t *testing.T) {
	t.Parallel()

	rootPath := privatePublicationRoot(t)
	root, err := openBackupRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()

	for _, name := range []string{"source", "destination"} {
		if err := unix.Mkdirat(root.descriptor, name, privateDirectoryMode); err != nil {
			t.Fatal(err)
		}
	}
	if err := renameNoReplace(root.descriptor, "source", "destination"); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("renameNoReplace conflict = %v", err)
	}
}
