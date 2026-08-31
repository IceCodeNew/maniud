//go:build linux || darwin

package cli

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	llmConfigLockName = ".env.lock"
	llmDirectoryMode  = 0o700
	llmFileMode       = 0o600
)

type llmConfigOperations struct {
	openRoot      func(string) (*os.Root, error)
	closeRoot     func(*os.Root) error
	lstat         func(*os.Root, string) (os.FileInfo, error)
	mkdir         func(*os.Root, string, os.FileMode) error
	openSubRoot   func(*os.Root, string) (*os.Root, error)
	statRoot      func(*os.Root, string) (os.FileInfo, error)
	openFile      func(*os.Root, string, int, os.FileMode) (*os.File, error)
	readFile      func(*os.File) ([]byte, error)
	statFile      func(*os.File) (os.FileInfo, error)
	writeFile     func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	closeFile     func(*os.File) error
	sameFile      func(os.FileInfo, os.FileInfo) bool
	flock         func(int, int) error
	rename        func(*os.Root, string, string) error
	remove        func(*os.Root, string) error
	openDirectory func(*os.Root, string) (*os.File, error)
}

func defaultLLMConfigOperations() llmConfigOperations {
	return llmConfigOperations{
		openRoot:    os.OpenRoot,
		closeRoot:   (*os.Root).Close,
		lstat:       (*os.Root).Lstat,
		mkdir:       (*os.Root).Mkdir,
		openSubRoot: (*os.Root).OpenRoot,
		statRoot:    (*os.Root).Stat,
		openFile:    (*os.Root).OpenFile,
		readFile: func(file *os.File) ([]byte, error) {
			return io.ReadAll(io.LimitReader(file, maximumLLMEnvBytes+1))
		},
		statFile:  (*os.File).Stat,
		writeFile: (*os.File).Write,
		syncFile:  (*os.File).Sync,
		closeFile: (*os.File).Close,
		sameFile:  os.SameFile,
		flock:     unix.Flock,
		rename:    (*os.Root).Rename,
		remove:    (*os.Root).Remove,
		openDirectory: func(root *os.Root, name string) (*os.File, error) {
			return root.Open(name)
		},
	}
}

//nolint:cyclop // The current-directory source has distinct regular-file and secret permission contracts.
func readCurrentLLMEnv(workingDirectory string) (llmEnvState, string) {
	if workingDirectory == "" || !filepath.IsAbs(workingDirectory) ||
		filepath.Clean(workingDirectory) != workingDirectory {
		return llmEnvState{}, llmSourceWarning("current .env", "working directory is invalid")
	}
	root, err := os.OpenRoot(workingDirectory)
	if err != nil {
		return llmEnvState{}, llmSourceWarning("current .env", "working directory is unavailable")
	}
	defer root.Close() //nolint:errcheck // A read-only diagnostic source has no pending write to settle.
	state, reason := readLLMEnvEntry(root, false)
	if reason != "" || !state.exists {
		return state, llmSourceWarningIfExists("current .env", state.exists, reason)
	}
	values, parseErr := parseLLMEnv(state.raw)
	if parseErr != nil {
		state.valid = false

		return state, llmSourceWarning("current .env", "content is malformed")
	}
	state.values = values
	if _, openAI := values[openAIKeyEnvironment]; openAI || hasKey(values, deepSeekKeyEnvironment) {
		if state.identity.owner != effectiveUserID() || state.mode.Perm()&0o077 != 0 {
			state.valid = false

			return state, llmSourceWarning("current .env", "API key permissions are unsafe")
		}
	}
	state.valid = true

	return state, ""
}

func hasKey(values map[string]string, name string) bool {
	_, found := values[name]

	return found
}

func readXDGLLMEnv(environment map[string]string) (llmEnvState, string) {
	root, err := openLLMConfigRoot(environment, false)
	if errors.Is(err, os.ErrNotExist) {
		return llmEnvState{valid: true}, ""
	}
	if err != nil {
		return llmEnvState{}, llmSourceWarning("XDG .env", "configuration path is unsafe")
	}
	defer root.Close() //nolint:errcheck // A read-only diagnostic source has no pending write to settle.
	state, reason := readLLMEnvEntry(root, true)
	if reason != "" || !state.exists {
		return state, llmSourceWarningIfExists("XDG .env", state.exists, reason)
	}
	values, parseErr := parseLLMEnv(state.raw)
	if parseErr != nil {
		state.valid = false

		return state, llmSourceWarning("XDG .env", "content is malformed")
	}
	state.values = values
	state.valid = true

	return state, ""
}

func llmSourceWarningIfExists(source string, exists bool, reason string) string {
	if !exists || reason == "" {
		return ""
	}

	return llmSourceWarning(source, reason)
}

func readLLMEnvEntry(root *os.Root, xdg bool) (llmEnvState, string) {
	return readLLMEnvEntryWithOperations(root, xdg, defaultLLMConfigOperations())
}

//nolint:cyclop // Descriptor, visible-entry, ownership, size, and reread checks form one file identity proof.
func readLLMEnvEntryWithOperations(
	root *os.Root,
	xdg bool,
	operations llmConfigOperations,
) (llmEnvState, string) {
	info, err := operations.lstat(root, llmConfigName)
	if errors.Is(err, os.ErrNotExist) {
		return llmEnvState{valid: true}, ""
	}
	state := llmEnvState{exists: true}
	if err != nil || !info.Mode().IsRegular() {
		return state, "entry is not a regular file"
	}
	identity, valid := llmIdentity(info)
	if !valid || identity.links != 1 {
		return state, "entry identity is unsafe"
	}
	permissions := info.Mode().Perm()
	if xdg {
		if identity.owner != effectiveUserID() || permissions != llmFileMode {
			return state, "entry permissions are unsafe"
		}
	} else if identity.owner != 0 && identity.owner != effectiveUserID() || permissions&0o022 != 0 {
		return state, "entry permissions are unsafe"
	}
	if info.Size() < 0 || info.Size() > maximumLLMEnvBytes {
		return state, "entry is too large"
	}
	file, err := operations.openFile(root, llmConfigName, os.O_RDONLY, 0)
	if err != nil {
		return state, "entry is unavailable"
	}
	raw, readErr := operations.readFile(file)
	descriptorInfo, statErr := operations.statFile(file)
	closeErr := operations.closeFile(file)
	visible, visibleErr := operations.lstat(root, llmConfigName)
	if readErr != nil || statErr != nil || closeErr != nil || visibleErr != nil ||
		len(raw) > maximumLLMEnvBytes || !operations.sameFile(info, descriptorInfo) ||
		!operations.sameFile(info, visible) {
		return state, "entry changed while reading"
	}
	state.mode = info.Mode().Perm()
	state.identity = identity
	state.digest = sha256Bytes(raw)
	state.raw = raw
	state.valid = true

	return state, ""
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}

func llmIdentity(info os.FileInfo) (llmFileIdentity, bool) {
	metadata, valid := info.Sys().(*syscall.Stat_t)
	if !valid {
		return llmFileIdentity{}, false
	}

	return llmFileIdentity{
		device: uint64(metadata.Dev), //nolint:gosec // Unix device IDs are non-negative kernel identifiers.
		inode:  metadata.Ino,
		links:  uint64(metadata.Nlink), owner: metadata.Uid,
	}, true
}

func effectiveUserID() uint32 {
	return uint32(os.Geteuid()) //nolint:gosec // Geteuid returns a non-negative platform uid_t.
}

func openLLMConfigRoot(environment map[string]string, create bool) (*os.Root, error) {
	return openLLMConfigRootWithOperations(environment, create, defaultLLMConfigOperations())
}

//nolint:cyclop,funlen // Anchored traversal and identity checks form one path-resolution operation.
func openLLMConfigRootWithOperations(
	environment map[string]string,
	create bool,
	operations llmConfigOperations,
) (*os.Root, error) {
	path, err := llmConfigRootPath(environment)
	if err != nil {
		return nil, err
	}
	root, err := operations.openRoot(string(filepath.Separator))
	if err != nil {
		return nil, fmt.Errorf("open configuration filesystem root: %w", err)
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		info, statErr := operations.lstat(root, component)
		if errors.Is(statErr, os.ErrNotExist) && create {
			if mkdirErr := operations.mkdir(root, component, llmDirectoryMode); mkdirErr != nil {
				_ = operations.closeRoot(root)

				return nil, fmt.Errorf("create configuration directory: %w", mkdirErr)
			}
			info, statErr = operations.lstat(root, component)
		}
		if statErr != nil {
			_ = operations.closeRoot(root)

			return nil, fmt.Errorf("inspect configuration directory: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = operations.closeRoot(root)

			return nil, errLLMConfigPathInvalid
		}
		next, openErr := operations.openSubRoot(root, component)
		if openErr != nil {
			_ = operations.closeRoot(root)

			return nil, fmt.Errorf("open configuration directory: %w", openErr)
		}
		anchored, anchorErr := operations.statRoot(next, ".")
		if anchorErr != nil || !operations.sameFile(info, anchored) {
			_ = operations.closeRoot(next)
			_ = operations.closeRoot(root)

			return nil, errLLMConfigPathInvalid
		}
		if closeErr := operations.closeRoot(root); closeErr != nil {
			_ = operations.closeRoot(next)

			return nil, fmt.Errorf("close configuration directory: %w", closeErr)
		}
		root = next
		if index == len(components)-1 {
			identity, identityValid := llmIdentity(anchored)
			if !identityValid || identity.owner != effectiveUserID() || anchored.Mode().Perm() != llmDirectoryMode {
				_ = operations.closeRoot(root)

				return nil, errLLMConfigPathInvalid
			}
		}
	}

	return root, nil
}

func publishXDGLLMEnv(
	environment map[string]string,
	baseline llmConfigBaseline,
	updates map[string]*string,
) (returnErr error) {
	return publishXDGLLMEnvWithOperations(
		environment, baseline, updates, defaultLLMConfigOperations(),
	)
}

//nolint:cyclop,funlen // Lock, compare, atomic publication, revalidation, and fsync are one save transaction.
func publishXDGLLMEnvWithOperations(
	environment map[string]string,
	baseline llmConfigBaseline,
	updates map[string]*string,
	operations llmConfigOperations,
) (returnErr error) {
	if !baseline.initialized || !baseline.state.valid {
		return errLLMConfigSaveStale
	}
	root, err := openLLMConfigRootWithOperations(environment, true, operations)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeRoot(root)) }()
	lock, err := openLLMConfigLockWithOperations(root, operations)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeFile(lock)) }()
	if err = operations.flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock LLM configuration: %w", err)
	}
	defer operations.flock( //nolint:errcheck // Closing the descriptor also releases the lock.
		int(lock.Fd()), unix.LOCK_UN,
	)
	current, reason := readLLMEnvEntryWithOperations(root, true, operations)
	if reason != "" || !sameLLMEnvState(current, baseline.state) {
		return errLLMConfigSaveStale
	}
	candidate, err := rewriteLLMEnv(current.raw, updates)
	if err != nil {
		return err
	}
	temporary := ".env.maniud-" + rand.Text()
	file, err := operations.openFile(root, temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, llmFileMode)
	if err != nil {
		return fmt.Errorf("create temporary LLM configuration: %w", err)
	}
	published := false
	defer func() {
		if !published {
			returnErr = errors.Join(returnErr, operations.remove(root, temporary))
		}
	}()
	written, writeErr := operations.writeFile(file, candidate)
	if writeErr == nil && written != len(candidate) {
		writeErr = io.ErrShortWrite
	}
	syncErr := operations.syncFile(file)
	closeErr := operations.closeFile(file)
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.Join(writeErr, syncErr, closeErr)
	}
	current, reason = readLLMEnvEntryWithOperations(root, true, operations)
	if reason != "" || !sameLLMEnvState(current, baseline.state) {
		return errLLMConfigSaveStale
	}
	if err = operations.rename(root, temporary, llmConfigName); err != nil {
		return fmt.Errorf("publish LLM configuration: %w", err)
	}
	published = true
	state, reason := readLLMEnvEntryWithOperations(root, true, operations)
	if reason != "" || !state.exists || !bytes.Equal(state.raw, candidate) {
		return errLLMConfigSaveUnknown
	}
	if err = syncLLMConfigDirectoryWithOperations(root, operations); err != nil {
		if settleErr := syncLLMConfigDirectoryWithOperations(root, operations); settleErr != nil {
			return errLLMConfigSaveUnknown
		}
	}

	return nil
}

func openLLMConfigLock(root *os.Root) (*os.File, error) {
	return openLLMConfigLockWithOperations(root, defaultLLMConfigOperations())
}

//nolint:cyclop // Visible-entry and descriptor identity checks prevent symlink or replacement lock races.
func openLLMConfigLockWithOperations(root *os.Root, operations llmConfigOperations) (*os.File, error) {
	before, beforeErr := operations.lstat(root, llmConfigLockName)
	if beforeErr != nil && !errors.Is(beforeErr, os.ErrNotExist) {
		return nil, errLLMConfigPathInvalid
	}
	if beforeErr == nil && !validLLMLockInfo(before) {
		return nil, errLLMConfigPathInvalid
	}
	lock, err := operations.openFile(root, llmConfigLockName, os.O_CREATE|os.O_RDWR, llmFileMode)
	if err != nil {
		return nil, fmt.Errorf("open LLM configuration lock: %w", err)
	}
	descriptor, descriptorErr := operations.statFile(lock)
	visible, visibleErr := operations.lstat(root, llmConfigLockName)
	if descriptorErr != nil || visibleErr != nil || !operations.sameFile(descriptor, visible) ||
		beforeErr == nil && !operations.sameFile(before, descriptor) || !validLLMLockInfo(descriptor) {
		_ = operations.closeFile(lock)

		return nil, errLLMConfigPathInvalid
	}

	return lock, nil
}

func validLLMLockInfo(info os.FileInfo) bool {
	identity, valid := llmIdentity(info)

	return valid && info.Mode().IsRegular() && identity.links == 1 &&
		identity.owner == effectiveUserID() && info.Mode().Perm() == llmFileMode
}

func syncLLMConfigDirectory(root *os.Root) error {
	return syncLLMConfigDirectoryWithOperations(root, defaultLLMConfigOperations())
}

func syncLLMConfigDirectoryWithOperations(root *os.Root, operations llmConfigOperations) error {
	directory, err := operations.openDirectory(root, ".")
	if err != nil {
		return fmt.Errorf("open LLM configuration directory: %w", err)
	}

	return errors.Join(operations.syncFile(directory), operations.closeFile(directory))
}
