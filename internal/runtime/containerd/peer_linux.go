//go:build linux

package containerd

import (
	"bytes"
	"io"
	"net"
	"os"
	"strconv"
	"syscall"
)

const (
	maximumProcessStatBytes = 4096
	processStartTimeIndex   = 19
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
	generation, err := processStartTime(credentials.Pid)
	if err != nil {
		return peerIdentity{}, err
	}

	return peerIdentity{
		process:    uint64(credentials.Pid),
		owner:      credentials.Uid,
		group:      credentials.Gid,
		generation: generation,
	}, nil
}

func processStartTime(process int32) (uint64, error) {
	file, err := os.Open("/proc/" + strconv.FormatInt(int64(process), 10) + "/stat")
	if err != nil {
		return 0, ErrInvalidEndpoint
	}

	return readProcessStartTime(file)
}

func readProcessStartTime(file io.ReadCloser) (uint64, error) {
	value, readErr := io.ReadAll(io.LimitReader(file, maximumProcessStatBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(value) > maximumProcessStatBytes {
		return 0, ErrInvalidEndpoint
	}

	return decodeProcessStartTime(value)
}

func decodeProcessStartTime(value []byte) (uint64, error) {
	closing := bytes.LastIndexByte(value, ')')
	if closing < 0 || closing+2 >= len(value) {
		return 0, ErrInvalidEndpoint
	}
	fields := bytes.Fields(value[closing+2:])
	if len(fields) <= processStartTimeIndex {
		return 0, ErrInvalidEndpoint
	}
	generation, err := strconv.ParseUint(string(fields[processStartTimeIndex]), 10, 64)
	if err != nil || generation == 0 {
		return 0, ErrInvalidEndpoint
	}

	return generation, nil
}
