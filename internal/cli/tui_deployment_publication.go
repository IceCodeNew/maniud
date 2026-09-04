package cli

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type deploymentEntryOperations struct {
	openRoot      func(string) (*os.Root, error)
	lstat         func(*os.Root, string) (os.FileInfo, error)
	readFile      func(*os.File) ([]byte, error)
	openFile      func(*os.Root, string, os.FileMode) (*os.File, error)
	writeFile     func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	closeFile     func(*os.File) error
	exchange      func(*os.File, string, string) error
	openDirectory func(*os.Root, string) (*os.File, error)
	remove        func(*os.Root, string) error
	closeRoot     func(*os.Root) error
}

type deploymentEntrySnapshot struct {
	info    os.FileInfo
	file    *os.File
	content []byte
}

func (snapshot deploymentEntrySnapshot) same(other deploymentEntrySnapshot) bool {
	return snapshot.info != nil && other.info != nil && os.SameFile(snapshot.info, other.info) &&
		bytes.Equal(snapshot.content, other.content)
}

func (snapshot deploymentEntrySnapshot) close() {
	if snapshot.file == nil {
		return
	}

	_ = snapshot.file.Close()
}

func defaultDeploymentEntryOperations() deploymentEntryOperations {
	return deploymentEntryOperations{
		openRoot: os.OpenRoot,
		lstat:    (*os.Root).Lstat,
		readFile: func(file *os.File) ([]byte, error) {
			return io.ReadAll(file)
		},
		openFile: func(root *os.Root, name string, mode os.FileMode) (*os.File, error) {
			return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		},
		writeFile: (*os.File).Write,
		syncFile:  (*os.File).Sync,
		closeFile: (*os.File).Close,
		exchange:  exchangeDeploymentEntries,
		openDirectory: func(root *os.Root, name string) (*os.File, error) {
			return root.Open(name)
		},
		remove:    (*os.Root).Remove,
		closeRoot: (*os.Root).Close,
	}
}

func replaceDeploymentEntry(
	repository string,
	entry string,
	before []byte,
	after []byte,
) (published bool, returnErr error) {
	return replaceDeploymentEntryWithOperations(
		repository, entry, before, after, defaultDeploymentEntryOperations(),
	)
}

//nolint:cyclop,funlen // Each filesystem step contributes to one exchange, proof, and rollback transaction.
func replaceDeploymentEntryWithOperations(
	repository string,
	entry string,
	before []byte,
	after []byte,
	operations deploymentEntryOperations,
) (published bool, returnErr error) {
	root, err := operations.openRoot(repository)
	if err != nil {
		return false, errDeploymentEditInvalid
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeRoot(root)) }()
	name := filepath.FromSlash(entry)
	beforeSnapshot := readDeploymentEntry(root, name, operations)
	defer beforeSnapshot.close()
	if beforeSnapshot.info == nil || !bytes.Equal(beforeSnapshot.content, before) {
		return false, errDeploymentEditInvalid
	}
	directory := filepath.Dir(name)
	temporary := filepath.Join(directory, "."+filepath.Base(name)+".maniud-"+rand.Text())
	file, err := operations.openFile(root, temporary, beforeSnapshot.info.Mode().Perm())
	if err != nil {
		return false, fmt.Errorf("create deployment edit: %w", err)
	}
	temporaryOwned := true
	defer func() {
		if temporaryOwned {
			returnErr = errors.Join(returnErr, operations.remove(root, temporary))
		}
	}()
	written, writeErr := operations.writeFile(file, after)
	if writeErr == nil && written != len(after) {
		writeErr = io.ErrShortWrite
	}
	syncErr := operations.syncFile(file)
	closeErr := operations.closeFile(file)
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return false, errors.Join(writeErr, syncErr, closeErr)
	}
	candidateSnapshot := readDeploymentEntry(root, temporary, operations)
	defer candidateSnapshot.close()
	if candidateSnapshot.info == nil || !bytes.Equal(candidateSnapshot.content, after) {
		return false, errDeploymentEditInvalid
	}
	directoryFile, err := operations.openDirectory(root, directory)
	if err != nil {
		return false, fmt.Errorf("open deployment edit directory: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeFile(directoryFile)) }()
	if err = operations.exchange(directoryFile, filepath.Base(temporary), filepath.Base(name)); err != nil {
		return false, fmt.Errorf("publish deployment edit: %w", err)
	}
	published = true
	temporaryOwned = false
	displacedSnapshot := readDeploymentEntry(root, temporary, operations)
	defer displacedSnapshot.close()
	publishedSnapshot := readDeploymentEntry(root, name, operations)
	defer publishedSnapshot.close()
	if !beforeSnapshot.same(displacedSnapshot) || !candidateSnapshot.same(publishedSnapshot) {
		rollbackErr := errDeploymentEditInvalid
		if candidateSnapshot.same(publishedSnapshot) {
			published, temporaryOwned, rollbackErr = rollbackDeploymentEntryExchange(
				root, directoryFile, name, temporary, displacedSnapshot, candidateSnapshot, operations,
			)
		}
		syncErr = operations.syncFile(directoryFile)

		return published, errors.Join(errDeploymentEditInvalid, rollbackErr, syncErr)
	}
	if err = operations.remove(root, temporary); err != nil {
		return true, fmt.Errorf("remove displaced deployment entry: %w", err)
	}
	if err = operations.syncFile(directoryFile); err != nil {
		return true, fmt.Errorf("sync deployment edit directory: %w", err)
	}

	return true, nil
}

//nolint:cyclop // Descriptor and two visible-path checks form one file identity proof.
func readDeploymentEntry(
	root *os.Root,
	name string,
	operations deploymentEntryOperations,
) deploymentEntrySnapshot {
	file, err := root.Open(name)
	if err != nil {
		return deploymentEntrySnapshot{}
	}
	info, statErr := file.Stat()
	visible, visibleErr := operations.lstat(root, name)
	current, err := operations.readFile(file)
	after, lstatErr := operations.lstat(root, name)
	if statErr != nil || visibleErr != nil || err != nil || lstatErr != nil ||
		!info.Mode().IsRegular() || !visible.Mode().IsRegular() || !after.Mode().IsRegular() ||
		!os.SameFile(info, visible) || !os.SameFile(info, after) {
		_ = file.Close()

		return deploymentEntrySnapshot{}
	}

	return deploymentEntrySnapshot{info: info, file: file, content: current}
}

func rollbackDeploymentEntryExchange(
	root *os.Root,
	directory *os.File,
	name string,
	temporary string,
	displaced deploymentEntrySnapshot,
	candidate deploymentEntrySnapshot,
	operations deploymentEntryOperations,
) (published bool, temporaryOwned bool, err error) {
	if err = operations.exchange(directory, filepath.Base(temporary), filepath.Base(name)); err != nil {
		return true, false, err
	}
	restored := readDeploymentEntry(root, name, operations)
	defer restored.close()
	leftover := readDeploymentEntry(root, temporary, operations)
	defer leftover.close()

	return !displaced.same(restored), candidate.same(leftover), nil
}
