//go:build darwin

package containerd

import (
	"net"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	peerTokenOwnerIndex      = 1
	peerTokenGroupIndex      = 2
	peerTokenProcessIndex    = 5
	peerTokenGenerationIndex = 7
	peerTokenWords           = 8
)

type peerToken [peerTokenWords]uint32

func socketMetadata(metadataValue os.FileInfo) (socketIdentity, bool) {
	stat, valid := metadataValue.Sys().(*syscall.Stat_t)
	if !valid {
		return socketIdentity{}, false
	}

	return socketIdentity{
		//nolint:gosec // Darwin stat.Dev is int32; device identity is stored as uint64.
		device: uint64(stat.Dev), inode: stat.Ino, owner: stat.Uid, group: stat.Gid, mode: uint32(stat.Mode),
		changeTime: stat.Ctimespec.Sec*1_000_000_000 + stat.Ctimespec.Nsec,
	}, true
}

func connectedPeer(connection net.Conn) (peerIdentity, error) {
	unixConnection, valid := connection.(*net.UnixConn)
	if !valid {
		return peerIdentity{}, ErrInvalidEndpoint
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return peerIdentity{}, ErrInvalidEndpoint
	}
	var token peerToken
	var socketErr error
	err = raw.Control(func(descriptor uintptr) {
		token, socketErr = connectedPeerToken(descriptor)
	})
	if err != nil || socketErr != nil || token[peerTokenProcessIndex] == 0 ||
		token[peerTokenGenerationIndex] == 0 {
		return peerIdentity{}, ErrInvalidEndpoint
	}

	return peerIdentity{
		process: uint64(token[peerTokenProcessIndex]), owner: token[peerTokenOwnerIndex],
		group: token[peerTokenGroupIndex], generation: uint64(token[peerTokenGenerationIndex]),
	}, nil
}

func connectedPeerToken(descriptor uintptr) (peerToken, error) {
	var token peerToken
	length := uint32(unsafe.Sizeof(token))
	_, _, callErr := unix.Syscall6(
		unix.SYS_GETSOCKOPT, //nolint:staticcheck // x/sys has no typed LOCAL_PEERTOKEN wrapper.
		descriptor,
		uintptr(unix.SOL_LOCAL),
		uintptr(unix.LOCAL_PEERTOKEN),
		uintptr(unsafe.Pointer(&token[0])), //nolint:gosec // getsockopt writes the fixed-size peer token.
		uintptr(unsafe.Pointer(&length)),   //nolint:gosec // getsockopt updates the token buffer length.
		0,
	)
	if callErr != 0 || uintptr(length) != unsafe.Sizeof(token) {
		return peerToken{}, ErrInvalidEndpoint
	}

	return token, nil
}
