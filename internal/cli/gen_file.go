package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const generatedFileMode = os.FileMode(0o600)

type generatedComposeOperations struct {
	openRoot      func(string) (*os.Root, error)
	openDirectory func(*os.Root) (*os.File, error)
	openFile      func(*os.Root, string) (*os.File, error)
	statFile      func(*os.File) (os.FileInfo, error)
	chmodFile     func(*os.File, os.FileMode) error
	writeFile     func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	closeFile     func(*os.File) error
	syncDirectory func(*os.File) error
	lstat         func(*os.Root, string) (os.FileInfo, error)
	remove        func(*os.Root, string) error
	closeRoot     func(*os.Root) error
}

func generatedComposeDefaultOperations() generatedComposeOperations {
	return generatedComposeOperations{
		openRoot: os.OpenRoot,
		openDirectory: func(root *os.Root) (*os.File, error) {
			return root.Open(".")
		},
		openFile: func(root *os.Root, name string) (*os.File, error) {
			return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, generatedFileMode)
		},
		statFile:      (*os.File).Stat,
		chmodFile:     (*os.File).Chmod,
		writeFile:     (*os.File).Write,
		syncFile:      (*os.File).Sync,
		closeFile:     (*os.File).Close,
		syncDirectory: (*os.File).Sync,
		lstat:         (*os.Root).Lstat,
		remove:        (*os.Root).Remove,
		closeRoot:     (*os.Root).Close,
	}
}

func writeGeneratedCompose(path string, content []byte) (returnErr error) {
	return writeGeneratedComposeWithOperations(path, content, generatedComposeDefaultOperations())
}

func writeGeneratedComposeWithOperations(
	path string,
	content []byte,
	operations generatedComposeOperations,
) (returnErr error) {
	return writeGeneratedArtifacts([]generatedArtifact{{path: path, content: content}}, operations)
}

type generatedArtifact struct {
	path    string
	content []byte
}

func writeGeneratedFiles(generated generatedCompose) error {
	artifacts := []generatedArtifact{{path: generated.absolutePath, content: generated.content}}
	if len(generated.preparation) != 0 {
		artifacts = append(artifacts, generatedArtifact{
			path: generated.preparationAbsolute, content: generated.preparation,
		})
	}

	return writeGeneratedArtifacts(artifacts, generatedComposeDefaultOperations())
}

func writeGeneratedArtifacts(
	artifacts []generatedArtifact,
	operations generatedComposeOperations,
) (returnErr error) {
	ownedFiles := make([]*generatedComposeFile, 0, len(artifacts))
	published := false
	defer func() {
		if !published {
			for _, owned := range ownedFiles {
				returnErr = errors.Join(returnErr, owned.remove())
			}
		}
		for _, owned := range ownedFiles {
			returnErr = errors.Join(returnErr, owned.close())
		}
	}()

	for _, artifact := range artifacts {
		owned, err := openGeneratedComposeWithOperations(artifact.path, operations)
		if err != nil {
			return err
		}
		ownedFiles = append(ownedFiles, owned)
	}
	for index, artifact := range artifacts {
		if err := ownedFiles[index].write(artifact.content); err != nil {
			return err
		}
	}
	published = true

	return nil
}

type generatedComposeFile struct {
	root       *os.Root
	directory  *os.File
	file       *os.File
	name       string
	identity   os.FileInfo
	operations generatedComposeOperations
}

func openGeneratedCompose(path string) (*generatedComposeFile, error) {
	return openGeneratedComposeWithOperations(path, generatedComposeDefaultOperations())
}

func openGeneratedComposeWithOperations(
	path string,
	operations generatedComposeOperations,
) (*generatedComposeFile, error) {
	root, err := operations.openRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open generated Compose directory: %w", err)
	}

	directory, err := operations.openDirectory(root)
	if err != nil {
		_ = operations.closeRoot(root)

		return nil, fmt.Errorf("retain generated Compose directory: %w", err)
	}

	file, err := operations.openFile(root, filepath.Base(path))
	if err != nil {
		_ = operations.closeFile(directory)
		_ = operations.closeRoot(root)

		return nil, fmt.Errorf("create generated Compose: %w", err)
	}

	identity, err := operations.statFile(file)
	if err != nil {
		_ = operations.closeFile(file)
		_ = operations.closeFile(directory)
		_ = operations.closeRoot(root)

		return nil, fmt.Errorf("inspect generated Compose: %w", err)
	}

	return &generatedComposeFile{
		root:       root,
		directory:  directory,
		file:       file,
		name:       filepath.Base(path),
		identity:   identity,
		operations: operations,
	}, nil
}

func (owned *generatedComposeFile) write(content []byte) error {
	if err := owned.operations.chmodFile(owned.file, generatedFileMode); err != nil {
		return fmt.Errorf("set generated Compose mode: %w", err)
	}
	written, err := owned.operations.writeFile(owned.file, content)
	if err != nil {
		return fmt.Errorf("write generated Compose: %w", err)
	}
	if written != len(content) {
		return fmt.Errorf("write generated Compose: %w", io.ErrShortWrite)
	}
	if err := owned.operations.syncFile(owned.file); err != nil {
		return fmt.Errorf("sync generated Compose: %w", err)
	}

	if err := owned.revalidate(int64(len(content))); err != nil {
		return err
	}
	if err := owned.operations.closeFile(owned.file); err != nil {
		return fmt.Errorf("close generated Compose: %w", err)
	}
	owned.file = nil
	if err := owned.operations.syncDirectory(owned.directory); err != nil {
		return fmt.Errorf("sync generated Compose directory: %w", err)
	}
	if err := owned.revalidate(int64(len(content))); err != nil {
		return err
	}

	return nil
}

func (owned *generatedComposeFile) revalidate(size int64) error {
	current, err := owned.operations.lstat(owned.root, owned.name)
	if err != nil {
		return errors.Join(fmt.Errorf("revalidate generated Compose: %w", err), runtimeargv.ErrInvalid)
	}
	if !os.SameFile(owned.identity, current) || current.Size() != size {
		return fmt.Errorf("generated Compose identity changed: %w", runtimeargv.ErrInvalid)
	}

	return nil
}

func (owned *generatedComposeFile) remove() error {
	current, err := owned.operations.lstat(owned.root, owned.name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect failed generated Compose: %w", err)
	}
	if !os.SameFile(owned.identity, current) {
		return fmt.Errorf("failed generated Compose ownership changed: %w", runtimeargv.ErrInvalid)
	}
	if err := owned.operations.remove(owned.root, owned.name); err != nil {
		return fmt.Errorf("remove failed generated Compose: %w", err)
	}
	if err := owned.operations.syncDirectory(owned.directory); err != nil {
		return fmt.Errorf("sync removed generated Compose: %w", err)
	}

	return nil
}

func (owned *generatedComposeFile) close() error {
	var errs []error
	if owned.file != nil {
		errs = append(errs, owned.operations.closeFile(owned.file))
	}
	errs = append(
		errs,
		owned.operations.closeFile(owned.directory),
		owned.operations.closeRoot(owned.root),
	)

	return errors.Join(errs...)
}
