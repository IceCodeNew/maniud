package cli

import (
	"context"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestWatchInterruptSubscribesToTerminationSignals(t *testing.T) {
	t.Parallel()

	notified := make(chan []os.Signal, 1)
	parent, cancel := context.WithCancel(context.Background())
	_, stop := watchInterrupt(parent, func(_ chan<- os.Signal, signals ...os.Signal) {
		notified <- signals
	})
	defer stop()
	defer cancel()

	select {
	case signals := <-notified:
		if !slices.Equal(signals, []os.Signal{os.Interrupt, syscall.SIGTERM}) {
			t.Fatalf("watchInterrupt() signals = %v", signals)
		}
	case <-time.After(time.Second):
		t.Fatal("interrupt watcher did not subscribe")
	}
}

func TestWatchInterruptCancelsOnFirstSignal(t *testing.T) {
	t.Parallel()

	ctx, stop := watchInterrupt(context.Background(), func(destination chan<- os.Signal, _ ...os.Signal) {
		go func() {
			destination <- syscall.SIGTERM
		}()
	})
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("first interrupt did not cancel new effects")
	}
}

func TestWatchInterruptStopsWhenParentEnds(t *testing.T) {
	t.Parallel()

	parent, cancel := context.WithCancel(context.Background())
	ctx, stop := watchInterrupt(parent, func(chan<- os.Signal, ...os.Signal) {})
	defer stop()
	cancel()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not stop interrupt watcher")
	}
}

func TestWatchInterruptExitsOnSecondSignal(t *testing.T) {
	t.Parallel()

	exited := make(chan int, 1)
	signals := make(chan os.Signal, 2)
	ctx, stop := watchInterruptWithExit(context.Background(), func(destination chan<- os.Signal, _ ...os.Signal) {
		go func() {
			destination <- os.Interrupt
			destination <- os.Interrupt
		}()
		_ = signals
	}, func(code int) {
		exited <- code
	})
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("first interrupt did not cancel new effects")
	}

	select {
	case code := <-exited:
		if code != domainCancelledExitStatus() {
			t.Fatalf("second interrupt exit = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("second interrupt did not exit")
	}
}
