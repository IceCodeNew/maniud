//go:build !linux && !darwin

package cli

import (
	"context"
	"errors"
)

var (
	errDaemonAlreadyRunning     = errors.New("daemon is already running")
	errDaemonControlUnavailable = errors.New("daemon control is unavailable")
)

type daemonLease struct{}

func acquireDaemonLease(string) (*daemonLease, bool, error) {
	return nil, false, errDaemonControlUnavailable
}

func requestDaemonStop(context.Context, string) (bool, error) {
	return false, errDaemonControlUnavailable
}

func daemonStopRequests(context.Context) (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

func (*daemonLease) Close() error {
	return nil
}
