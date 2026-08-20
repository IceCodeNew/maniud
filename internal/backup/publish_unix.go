//go:build linux || darwin

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
	"golang.org/x/sys/unix"
)

const partialRandomBytes = 16

// ArchiveInput supplies the stopped workload stream for one manifest target.
type ArchiveInput struct {
	Target       string
	Reader       io.Reader
	MaximumBytes int64
}

// Publication is one durably complete transaction backup.
type Publication struct {
	Manifest       Manifest
	ManifestPath   string
	ManifestDigest domain.Digest
}

type publicationEntry struct {
	name     string
	identity fileIdentity
}

type publicationOperations struct {
	mkdir         func(int, string, uint32) error
	openDirectory func(int, string, int, uint32) (int, error)
	openFile      func(int, string, int, uint32) (int, error)
	unlink        func(int, string, int) error
	writeFile     func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	closeFile     func(*os.File) error
	syncDirectory func(int) error
	rename        func(int, string, string) error
}

type partialPublication struct {
	root       *directoryAnchor
	name       string
	descriptor int
	identity   fileIdentity
	entries    []publicationEntry
	operations publicationOperations
}

func defaultPublicationOperations() publicationOperations {
	return publicationOperations{
		mkdir:         unix.Mkdirat,
		openDirectory: unix.Openat,
		openFile:      unix.Openat,
		unlink:        unix.Unlinkat,
		writeFile:     (*os.File).Write,
		syncFile:      (*os.File).Sync,
		closeFile:     (*os.File).Close,
		syncDirectory: unix.Fsync,
		rename:        renameNoReplace,
	}
}

// Publish writes verified artifacts when the destination still satisfies the
// capacity evidence prepared before the workload was stopped. It exposes the
// complete manifest and artifacts with one no-replace rename.
func Publish(
	ctx context.Context,
	rootPath string,
	manifest Manifest,
	archives []ArchiveInput,
	capacity PublicationCapacity,
) (Publication, error) {
	return publishWithPreparedOperations(
		ctx, rootPath, manifest, archives, rand.Reader, defaultPublicationOperations(), &capacity,
	)
}

func publish(
	ctx context.Context,
	rootPath string,
	manifest Manifest,
	archives []ArchiveInput,
	random io.Reader,
) (Publication, error) {
	return publishWithOperations(ctx, rootPath, manifest, archives, random, defaultPublicationOperations())
}

func publishWithOperations(
	ctx context.Context,
	rootPath string,
	manifest Manifest,
	archives []ArchiveInput,
	random io.Reader,
	operations publicationOperations,
) (Publication, error) {
	return publishWithPreparedOperations(ctx, rootPath, manifest, archives, random, operations, nil)
}

//nolint:cyclop // The linear publication lifecycle keeps ownership and cleanup state in one scope.
func publishWithPreparedOperations(
	ctx context.Context,
	rootPath string,
	manifest Manifest,
	archives []ArchiveInput,
	random io.Reader,
	operations publicationOperations,
	capacity *PublicationCapacity,
) (result Publication, resultErr error) {
	manifest.Artifacts = slices.Clone(manifest.Artifacts)
	ordered, valid := validateArchiveInputs(manifest, archives)
	if !valid || random == nil {
		return Publication{}, ErrInvalidManifest
	}
	if err := ctx.Err(); err != nil {
		return Publication{}, fmt.Errorf("publish backup: %w", err)
	}

	root, err := openBackupRoot(rootPath)
	if err != nil {
		return Publication{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.close())
	}()
	if capacity != nil {
		if err = validatePublicationCapacity(root.descriptor, manifest, *capacity); err != nil {
			return Publication{}, err
		}
	}

	partial, err := createPartial(root, manifest.TransactionID, random, operations)
	if err != nil {
		return Publication{}, err
	}
	published := false
	defer func() {
		if !published {
			resultErr = errors.Join(resultErr, partial.cleanup())
		} else {
			resultErr = errors.Join(resultErr, partial.close())
		}
	}()

	result, err = partial.stage(ctx, manifest, ordered)
	if err != nil {
		return Publication{}, err
	}
	published, err = partial.commit(ctx, result.Manifest)
	if err != nil {
		return Publication{}, err
	}

	return result, nil
}

func (partial *partialPublication) stage(
	ctx context.Context,
	manifest Manifest,
	archives []ArchiveInput,
) (Publication, error) {
	if err := partial.stageArchives(ctx, &manifest, archives); err != nil {
		return Publication{}, err
	}

	raw, digest, err := EncodeManifest(manifest)
	if err != nil {
		return Publication{}, err
	}
	if err = partial.finishStage(ctx, raw); err != nil {
		return Publication{}, err
	}

	path, _ := ManifestPath(manifest.TransactionID)

	return Publication{Manifest: manifest, ManifestPath: path, ManifestDigest: digest}, nil
}

func (partial *partialPublication) stageArchives(
	ctx context.Context,
	manifest *Manifest,
	archives []ArchiveInput,
) error {
	for index, input := range archives {
		inventory, err := partial.writeArchive(ctx, artifactName(index), input)
		if err != nil {
			return err
		}
		if !SameContent(manifest.Artifacts[index].Inventory, inventory) {
			return ErrInvalidArchive
		}
		manifest.Artifacts[index].Inventory = inventory
	}

	return nil
}

func (partial *partialPublication) finishStage(ctx context.Context, raw []byte) error {
	if err := partial.writeManifest(raw); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish backup: %w", err)
	}
	if !partial.valid() || partial.operations.syncDirectory(partial.descriptor) != nil ||
		!partial.valid() || !partial.root.valid() {
		return ErrInvalidBackupRoot
	}

	return nil
}

func (partial *partialPublication) commit(ctx context.Context, manifest Manifest) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("publish backup: %w", err)
	}

	finalName := manifest.TransactionID.String()
	raw := canonicalManifest(manifest)
	err := partial.operations.rename(partial.root.descriptor, partial.name, finalName)
	if errors.Is(err, unix.EEXIST) {
		if verifyExistingPublication(ctx, partial.root, finalName, raw, manifest, partial.operations) {
			return false, nil
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return false, fmt.Errorf("verify published backup: %w", contextErr)
		}

		return false, ErrPublicationConflict
	}
	if err != nil {
		return false, ErrInvalidBackupRoot
	}

	if partial.operations.syncDirectory(partial.root.descriptor) != nil || !partial.root.valid() ||
		!rootEntryMatches(partial.root, finalName, partial.identity) {
		return true, ErrPublicationUnknown
	}

	return true, nil
}

func validateArchiveInputs(manifest Manifest, archives []ArchiveInput) ([]ArchiveInput, bool) {
	if !validManifest(manifest) || len(archives) != len(manifest.Artifacts) {
		return nil, false
	}

	ordered := slices.Clone(archives)
	slices.SortFunc(ordered, func(left, right ArchiveInput) int {
		return strings.Compare(left.Target, right.Target)
	})
	for index, input := range ordered {
		if input.Reader == nil || input.MaximumBytes <= 0 || input.Target != manifest.Artifacts[index].Mount.Target ||
			index > 0 && ordered[index-1].Target == input.Target {
			return nil, false
		}
	}

	return ordered, true
}

func createPartial(
	root *directoryAnchor,
	transaction Identifier,
	random io.Reader,
	operations publicationOperations,
) (*partialPublication, error) {
	if root == nil || !root.valid() || transaction == (Identifier{}) {
		return nil, ErrInvalidBackupRoot
	}

	var nonce [partialRandomBytes]byte
	if _, err := io.ReadFull(random, nonce[:]); err != nil {
		return nil, ErrInvalidBackupRoot
	}
	name := "." + transaction.String() + ".partial." + hex.EncodeToString(nonce[:])
	createdIdentity, err := createPartialEntry(root, name, operations)
	if err != nil {
		return nil, err
	}

	return openPartial(root, name, createdIdentity, operations)
}

func createPartialEntry(
	root *directoryAnchor,
	name string,
	operations publicationOperations,
) (fileIdentity, error) {
	if err := operations.mkdir(root.descriptor, name, privateDirectoryMode); err != nil {
		return fileIdentity{}, ErrInvalidBackupRoot
	}
	createdIdentity, createdValid := entryIdentity(root.descriptor, name)
	if !createdValid || !privateDirectory(createdIdentity) {
		return fileIdentity{}, ErrInvalidBackupRoot
	}

	return createdIdentity, nil
}

func openPartial(
	root *directoryAnchor,
	name string,
	createdIdentity fileIdentity,
	operations publicationOperations,
) (*partialPublication, error) {
	descriptor, err := operations.openDirectory(
		root.descriptor,
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		cleanupErr := removeCreatedDirectory(root, name, createdIdentity, operations)

		return nil, errors.Join(ErrInvalidBackupRoot, cleanupErr)
	}
	identity, valid := descriptorIdentity(descriptor)
	partial := &partialPublication{
		root: root, name: name, descriptor: descriptor, identity: identity,
		entries: make([]publicationEntry, 0), operations: operations,
	}
	if !valid || !sameEntry(identity, createdIdentity) || !privateDirectory(identity) || !partial.valid() {
		_ = partial.close()
		cleanupErr := removeCreatedDirectory(root, name, createdIdentity, operations)

		return nil, errors.Join(ErrInvalidBackupRoot, cleanupErr)
	}

	return partial, nil
}

func removeCreatedDirectory(
	root *directoryAnchor,
	name string,
	expected fileIdentity,
	operations publicationOperations,
) error {
	identity, valid := entryIdentity(root.descriptor, name)
	if !root.valid() || !valid || !sameEntry(identity, expected) || !privateDirectory(identity) {
		return ErrInvalidBackupRoot
	}
	if operations.unlink(root.descriptor, name, unix.AT_REMOVEDIR) != nil ||
		operations.syncDirectory(root.descriptor) != nil {
		return ErrInvalidBackupRoot
	}

	return nil
}

func (partial *partialPublication) writeArchive(
	ctx context.Context,
	name string,
	input ArchiveInput,
) (Inventory, error) {
	file, identity, err := partial.createFile(name)
	if err != nil {
		return Inventory{}, err
	}
	partial.entries = append(partial.entries, publicationEntry{name: name, identity: identity})

	inventory, analyzeErr := Analyze(ctx, io.TeeReader(input.Reader, file), input.MaximumBytes)
	syncErr := partial.operations.syncFile(file)
	valid := partial.fileValid(file, name, identity)
	closeErr := partial.operations.closeFile(file)
	if analyzeErr != nil {
		return Inventory{}, errors.Join(analyzeErr, syncErr, closeErr)
	}
	if syncErr != nil || closeErr != nil || !valid {
		return Inventory{}, ErrInvalidBackupRoot
	}

	return inventory, nil
}

func (partial *partialPublication) writeManifest(raw []byte) error {
	file, identity, err := partial.createFile(manifestName)
	if err != nil {
		return err
	}
	partial.entries = append(partial.entries, publicationEntry{name: manifestName, identity: identity})

	written, writeErr := partial.operations.writeFile(file, raw)
	syncErr := partial.operations.syncFile(file)
	valid := written == len(raw) && partial.fileValid(file, manifestName, identity)
	closeErr := partial.operations.closeFile(file)
	if writeErr != nil || syncErr != nil || closeErr != nil || !valid {
		return ErrInvalidBackupRoot
	}

	return nil
}

func (partial *partialPublication) createFile(name string) (*os.File, fileIdentity, error) {
	if !partial.valid() {
		return nil, fileIdentity{}, ErrInvalidBackupRoot
	}

	descriptor, err := partial.operations.openFile(
		partial.descriptor,
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	if err != nil {
		return nil, fileIdentity{}, ErrInvalidBackupRoot
	}
	identity, valid := descriptorIdentity(descriptor)
	if !valid || !privateRegular(identity) {
		_ = unix.Close(descriptor)
		if valid {
			_ = partial.removeCreatedFile(name, identity)
		}

		return nil, fileIdentity{}, ErrInvalidBackupRoot
	}

	return os.NewFile(uintptr(descriptor), name), identity, nil
}

func (partial *partialPublication) removeCreatedFile(name string, expected fileIdentity) bool {
	identity, valid := entryIdentity(partial.descriptor, name)

	return partial.valid() && valid && sameEntry(identity, expected) && privateRegular(identity) &&
		partial.operations.unlink(partial.descriptor, name, 0) == nil
}

func (partial *partialPublication) valid() bool {
	if partial == nil || partial.root == nil || partial.descriptor < 0 || !partial.root.valid() {
		return false
	}
	descriptor, descriptorValid := descriptorIdentity(partial.descriptor)
	entry, entryValid := entryIdentity(partial.root.descriptor, partial.name)

	return descriptorValid && entryValid && sameNode(descriptor, partial.identity) &&
		sameEntry(entry, partial.identity) && privateDirectory(descriptor)
}

func (partial *partialPublication) fileValid(file *os.File, name string, expected fileIdentity) bool {
	if file == nil || !partial.valid() {
		return false
	}
	descriptor, descriptorValid := descriptorIdentity(int(file.Fd()))
	entry, entryValid := entryIdentity(partial.descriptor, name)

	return descriptorValid && entryValid && sameEntry(descriptor, expected) &&
		sameEntry(entry, expected) && privateRegular(descriptor)
}

func (partial *partialPublication) cleanup() error {
	if partial == nil || partial.descriptor < 0 {
		return nil
	}
	if !partial.valid() {
		return errors.Join(ErrInvalidBackupRoot, partial.close())
	}

	cleanupErr := partial.removeEntries()
	closeErr := partial.close()
	if cleanupErr != nil || closeErr != nil || !partial.root.valid() {
		return errors.Join(cleanupErr, closeErr, ErrInvalidBackupRoot)
	}
	if !partial.removeDirectory() {
		return ErrInvalidBackupRoot
	}

	return nil
}

func (partial *partialPublication) removeEntries() error {
	for _, entry := range slices.Backward(partial.entries) {
		identity, valid := entryIdentity(partial.descriptor, entry.name)
		if !valid || !sameEntry(identity, entry.identity) || !privateRegular(identity) ||
			partial.operations.unlink(partial.descriptor, entry.name, 0) != nil {
			return ErrInvalidBackupRoot
		}
	}
	if partial.operations.syncDirectory(partial.descriptor) != nil {
		return ErrInvalidBackupRoot
	}

	return nil
}

func (partial *partialPublication) removeDirectory() bool {
	identity, valid := entryIdentity(partial.root.descriptor, partial.name)

	return valid && sameEntry(identity, partial.identity) &&
		partial.operations.unlink(partial.root.descriptor, partial.name, unix.AT_REMOVEDIR) == nil &&
		partial.operations.syncDirectory(partial.root.descriptor) == nil
}

func (partial *partialPublication) close() error {
	if partial == nil || partial.descriptor < 0 {
		return nil
	}

	err := unix.Close(partial.descriptor)
	partial.descriptor = -1
	if err != nil {
		return fmt.Errorf("close partial backup directory: %w", err)
	}

	return nil
}

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
		_ = unix.Close(directory)

		return Publication{}, false, errors.Join(ErrInvalidArchive, root.close())
	}

	manifest, digest, decodeErr := DecodeManifest(bytes.NewReader(raw))
	if decodeErr != nil || manifest.TransactionID != transaction {
		_ = unix.Close(directory)

		return Publication{}, false, errors.Join(ErrInvalidManifest, root.close())
	}
	if !publicationNamesMatch(directory, manifest) ||
		!publicationContentsMatch(ctx, directory, raw, manifest, operations) ||
		!root.valid() {
		_ = unix.Close(directory)

		return Publication{}, false, errors.Join(ErrInvalidArchive, root.close())
	}

	path, _ := ManifestPath(transaction)
	closeErr := errors.Join(unix.Close(directory), root.close())
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
		_ = unix.Close(directory)
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
