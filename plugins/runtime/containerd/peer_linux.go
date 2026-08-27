//go:build linux

package containerd

import (
	"net"
	"os"
	"syscall"

	"github.com/IceCodeNew/maniud/internal/procfs"
)

func socketMetadata(metadataValue os.FileInfo) (socketIdentity, bool) {
	stat, valid := metadataValue.Sys().(*syscall.Stat_t)
	if !valid {
		return socketIdentity{}, false
	}

	return socketIdentity{
		device: stat.Dev, inode: stat.Ino, owner: stat.Uid, group: stat.Gid, mode: stat.Mode,
		changeTime: stat.Ctim.Sec*1_000_000_000 + stat.Ctim.Nsec,
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
		process:    uint64(credentials.Pid),
		owner:      credentials.Uid,
		group:      credentials.Gid,
		generation: generation,
	}, nil
}
