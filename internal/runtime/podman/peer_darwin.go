//go:build darwin

package podman

import (
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func socketMetadata(metadata os.FileInfo) (socketIdentity, bool) {
	stat, valid := metadata.Sys().(*syscall.Stat_t)
	if !valid {
		return socketIdentity{}, false
	}

	return socketIdentity{
		//nolint:gosec // Darwin stat.Dev is int32; device identity is stored as uint64.
		device: uint64(stat.Dev), inode: stat.Ino, owner: stat.Uid, mode: uint32(stat.Mode),
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
	var process int
	var credentials *unix.Xucred
	var socketErr error
	err = raw.Control(func(descriptor uintptr) {
		process, socketErr = unix.GetsockoptInt(int(descriptor), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if socketErr == nil {
			credentials, socketErr = unix.GetsockoptXucred(int(descriptor), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		}
	})
	if err != nil || socketErr != nil || credentials == nil || process <= 0 || credentials.Ngroups < 1 {
		return peerIdentity{}, ErrInvalidEndpoint
	}

	return peerIdentity{
		process: int32(process), owner: credentials.Uid,
		group: credentials.Groups[0], generation: uint64(process),
	}, nil
}
