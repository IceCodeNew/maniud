package imagearchive

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type fileIdentity struct {
	device     uint64
	inode      uint64
	size       int64
	mode       os.FileMode
	modifiedNS int64
	changedNS  int64
}

type sourceOpenOperations struct {
	lstat func(string) (os.FileInfo, error)
	open  func(string, int, uint32) (int, error)
	stat  func(*os.File) (os.FileInfo, error)
	close func(*os.File) error
}

func openSource(name string) (*os.File, fileIdentity, error) {
	return openSourceWithOperations(name, sourceOpenOperations{
		lstat: os.Lstat,
		open:  syscall.Open,
		stat:  (*os.File).Stat,
		close: (*os.File).Close,
	})
}

func openSourceWithOperations(
	name string,
	operations sourceOpenOperations,
) (*os.File, fileIdentity, error) {
	var empty fileIdentity
	metadata, err := operations.lstat(name)
	if err != nil || !metadata.Mode().IsRegular() || metadata.Size() <= 0 || metadata.Size() > maximumArchiveBytes {
		return nil, empty, ErrInvalidSource
	}
	before, valid := identity(metadata)
	if !valid {
		return nil, empty, ErrInvalidSource
	}

	descriptor, err := operations.open(
		name,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, empty, ErrInvalidSource
	}
	file := os.NewFile(uintptr(descriptor), filepath.Base(name))
	opened, err := operations.stat(file)
	openedIdentity, valid := identity(opened)
	if err != nil || !valid || openedIdentity != before {
		closeErr := operations.close(file)

		return nil, empty, errors.Join(ErrInvalidSource, closeErr)
	}

	return file, before, nil
}

func identity(info os.FileInfo) (fileIdentity, bool) {
	if info == nil || !info.Mode().IsRegular() {
		return fileIdentity{}, false
	}
	value, valid := info.Sys().(*syscall.Stat_t)
	if !valid {
		return fileIdentity{}, false
	}

	return fileIdentity{
		device:     statDevice(value),
		inode:      statInode(value),
		size:       info.Size(),
		mode:       info.Mode(),
		modifiedNS: info.ModTime().UnixNano(),
		changedNS:  statChangeTime(value),
	}, true
}

func verifySourceIdentity(file *os.File, name string, before fileIdentity) error {
	opened, err := file.Stat()
	openedIdentity, openedValid := identity(opened)
	current, pathErr := os.Lstat(name)
	currentIdentity, currentValid := identity(current)
	if err != nil || pathErr != nil || !openedValid || !currentValid ||
		openedIdentity != before || currentIdentity != before {
		return ErrInvalidSource
	}

	return nil
}
