//go:build linux || darwin

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/IceCodeNew/maniud/internal/domain"
	"golang.org/x/sys/unix"
)

// Open returns one complete transaction publication after proving the private
// directory, manifest, and artifact identities. A missing directory is a typed
// absence; any other identity or content mismatch fails closed.
func Open(ctx context.Context, rootPath string, transaction Identifier) (Publication, bool, error) {
	return openPublication(ctx, rootPath, transaction, defaultPublicationOperations())
}

func openPublication(
	ctx context.Context,
	rootPath string,
	transaction Identifier,
	operations publicationOperations,
) (Publication, bool, error) {
	root, found, err := existingPublicationRoot(ctx, rootPath, transaction)
	if err != nil {
		return Publication{}, false, err
	}
	if !found || root == nil {
		return Publication{}, false, nil
	}

	directory, _, valid := openPublicationDirectory(root, transaction.String())
	if !valid {
		return Publication{}, false, root.close()
	}

	return readPublication(ctx, root, directory, transaction, operations)
}

func existingPublicationRoot(
	ctx context.Context,
	rootPath string,
	transaction Identifier,
) (*directoryAnchor, bool, error) {
	if transaction == (Identifier{}) {
		return nil, false, ErrInvalidManifest
	}
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("open backup: %w", err)
	}

	root, found := openExistingBackupRoot(rootPath)
	if !found {
		return nil, false, nil
	}

	return root, true, nil
}

func readPublication(
	ctx context.Context,
	root *directoryAnchor,
	directory int,
	transaction Identifier,
	operations publicationOperations,
) (Publication, bool, error) {
	raw, readable := readPrivateFile(ctx, directory, manifestName, maximumManifestBytes, operations)
	if !readable {
		_ = operations.closeDescriptor(directory)

		return Publication{}, false, errors.Join(ErrInvalidArchive, root.close())
	}

	manifest, digest, decodeErr := DecodeManifest(bytes.NewReader(raw))
	if decodeErr != nil || manifest.TransactionID != transaction {
		_ = operations.closeDescriptor(directory)

		return Publication{}, false, errors.Join(ErrInvalidManifest, root.close())
	}
	if !publicationNamesMatch(directory, manifest) ||
		!publicationContentsMatch(ctx, directory, raw, manifest, operations) ||
		!root.valid() {
		_ = operations.closeDescriptor(directory)

		return Publication{}, false, errors.Join(ErrInvalidArchive, root.close())
	}

	path, _ := ManifestPath(transaction)
	closeErr := errors.Join(operations.closeDescriptor(directory), root.close())
	if closeErr != nil {
		return Publication{}, false, closeErr
	}

	return Publication{Manifest: manifest, ManifestPath: path, ManifestDigest: digest}, true, nil
}

func verifyExistingPublication(
	ctx context.Context,
	root *directoryAnchor,
	name string,
	raw []byte,
	manifest Manifest,
	operations publicationOperations,
) bool {
	directory, identity, valid := openPublicationDirectory(root, name)
	if !valid {
		return false
	}
	defer func() {
		_ = operations.closeDescriptor(directory)
	}()

	if !publicationNamesMatch(directory, manifest) ||
		!publicationContentsMatch(ctx, directory, raw, manifest, operations) {
		return false
	}

	return root.valid() && rootEntryMatches(root, name, identity)
}

func openPublicationDirectory(root *directoryAnchor, name string) (int, fileIdentity, bool) {
	directory, err := unix.Openat(
		root.descriptor,
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return -1, fileIdentity{}, false
	}

	identity, valid := descriptorIdentity(directory)
	if !valid || !privateDirectory(identity) || !rootEntryMatches(root, name, identity) {
		_ = unix.Close(directory)

		return -1, fileIdentity{}, false
	}

	return directory, identity, true
}

func publicationNamesMatch(directory int, manifest Manifest) bool {
	wantNames := make([]string, 1, len(manifest.Artifacts)+1)
	wantNames[0] = manifestName
	for _, artifact := range manifest.Artifacts {
		wantNames = append(wantNames, artifact.FileName)
	}
	slices.Sort(wantNames)

	return slices.Equal(directoryNames(directory), wantNames)
}

func publicationContentsMatch(
	ctx context.Context,
	directory int,
	raw []byte,
	manifest Manifest,
	operations publicationOperations,
) bool {
	existingManifest, valid := readPrivateFile(
		ctx, directory, manifestName, maximumManifestBytes, operations,
	)
	if !valid || !bytes.Equal(existingManifest, raw) {
		return false
	}
	for _, artifact := range manifest.Artifacts {
		if !verifyArtifactFile(ctx, directory, artifact, operations) {
			return false
		}
	}

	return true
}

func directoryNames(descriptor int) []string {
	duplicate, err := unix.Dup(descriptor)
	if err != nil {
		return nil
	}
	file := os.NewFile(uintptr(duplicate), "backup directory")
	names, readErr := file.Readdirnames(-1)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil
	}
	slices.Sort(names)

	return names
}

func readPrivateFile(
	ctx context.Context,
	directory int,
	name string,
	maximum int64,
	operations publicationOperations,
) ([]byte, bool) {
	descriptor, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}
	file := os.NewFile(uintptr(descriptor), name)
	identity, valid := descriptorIdentity(descriptor)
	if !valid || !privateRegular(identity) {
		_ = operations.closeFile(file)

		return nil, false
	}

	value, readErr := io.ReadAll(io.LimitReader(&contextReader{cancelled: ctx.Err, source: file}, maximum+1))
	entry, entryValid := entryIdentity(directory, name)
	closeErr := operations.closeFile(file)

	return value, readErr == nil && closeErr == nil && int64(len(value)) <= maximum &&
		entryValid && sameEntry(entry, identity)
}

func verifyArtifactFile(
	ctx context.Context,
	directory int,
	artifact Artifact,
	operations publicationOperations,
) bool {
	descriptor, err := unix.Openat(
		directory,
		artifact.FileName,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(descriptor), artifact.FileName)
	identity, valid := descriptorIdentity(descriptor)
	if !valid || !privateRegular(identity) {
		_ = operations.closeFile(file)

		return false
	}

	hasher := sha256.New()
	written, readErr := io.Copy(hasher, io.LimitReader(
		&contextReader{cancelled: ctx.Err, source: file}, artifact.Inventory.ArchiveBytes+1,
	))
	entry, entryValid := entryIdentity(directory, artifact.FileName)
	closeErr := operations.closeFile(file)
	var digest domain.Digest
	copy(digest[:], hasher.Sum(nil))

	return readErr == nil && closeErr == nil && written == artifact.Inventory.ArchiveBytes &&
		digest == artifact.Inventory.ArchiveDigest && entryValid && sameEntry(entry, identity)
}

func rootEntryMatches(root *directoryAnchor, name string, expected fileIdentity) bool {
	identity, valid := entryIdentity(root.descriptor, name)

	return valid && sameNode(identity, expected) && privateDirectory(identity)
}

type contextReader struct {
	cancelled func() error
	source    io.Reader
}

func (reader *contextReader) Read(destination []byte) (int, error) {
	if err := reader.cancelled(); err != nil {
		return 0, fmt.Errorf("read published backup: %w", err)
	}

	count, err := reader.source.Read(destination)
	if errors.Is(err, io.EOF) {
		return count, io.EOF
	}
	if err != nil {
		return count, fmt.Errorf("read published backup: %w", err)
	}

	return count, nil
}
