//go:build linux

package podman

import (
	"net"
	"os"
	"syscall"

	"github.com/IceCodeNew/maniud/internal/procfs"
)

func socketMetadata(metadata os.FileInfo) (socketIdentity, bool) {
	stat, valid := metadata.Sys().(*syscall.Stat_t)
	if !valid {
		return socketIdentity{}, false
	}

	return socketIdentity{
		device: stat.Dev, inode: stat.Ino, owner: stat.Uid, mode: stat.Mode,
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
	var credentials *syscall.Ucred
	var socketErr error
	err = raw.Control(func(descriptor uintptr) {
		credentials, socketErr = syscall.GetsockoptUcred(
			int(descriptor), syscall.SOL_SOCKET, syscall.SO_PEERCRED,
		)
	})
	if err != nil || socketErr != nil {
		return peerIdentity{}, ErrInvalidEndpoint
	}

	return peerFromCredentials(credentials)
}

func peerFromCredentials(credentials *syscall.Ucred) (peerIdentity, error) {
	if credentials == nil || credentials.Pid <= 0 {
		return peerIdentity{}, ErrInvalidEndpoint
	}
	generation, valid := procfs.StartTime(credentials.Pid)
	if !valid {
		return peerIdentity{}, ErrInvalidEndpoint
	}

	return peerIdentity{
		process: credentials.Pid, owner: credentials.Uid, group: credentials.Gid, generation: generation,
	}, nil
}
