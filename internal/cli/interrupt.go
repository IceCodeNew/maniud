package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// CommandContext cancels new effects on the first termination signal and exits
// 130 on the second signal.
func CommandContext(parent context.Context) (context.Context, context.CancelFunc) {
	return watchInterrupt(parent, signalNotify)
}

func watchInterrupt(
	parent context.Context,
	notify func(chan<- os.Signal, ...os.Signal),
) (context.Context, context.CancelFunc) {
	return watchInterruptWithExit(parent, notify, os.Exit)
}

func watchInterruptWithExit(
	parent context.Context,
	notify func(chan<- os.Signal, ...os.Signal),
	exit func(int),
) (context.Context, context.CancelFunc) {
	ctx, stopNewEffects := context.WithCancel(parent)
	signals := make(chan os.Signal, interruptSignalBuffer)
	notify(signals, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-parent.Done():
			stopNewEffects()

			return
		case <-signals:
			stopNewEffects()
		}

		select {
		case <-parent.Done():
			return
		case <-signals:
			exit(domainCancelledExitStatus())
		}
	}()

	return ctx, stopNewEffects
}

const (
	interruptSignalBuffer      = 2
	cancelledCommandExitStatus = 130
)

func domainCancelledExitStatus() int {
	return cancelledCommandExitStatus
}

func signalNotify(destination chan<- os.Signal, signals ...os.Signal) {
	signal.Notify(destination, signals...)
}
