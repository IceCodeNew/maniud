package cli

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/notification"
)

const notificationDiagnosticQueueDepth = 32

type notificationDiagnosticSink struct {
	mutex     sync.RWMutex
	accepting bool
	queue     chan string
	done      chan struct{}
	stopOnce  sync.Once
	writer    io.Writer
}

func newNotificationDiagnosticSink(writer io.Writer) *notificationDiagnosticSink {
	if writer == nil {
		return nil
	}

	sink := &notificationDiagnosticSink{
		accepting: true,
		queue:     make(chan string, notificationDiagnosticQueueDepth),
		done:      make(chan struct{}),
		writer:    writer,
	}
	go sink.run()

	return sink
}

func (sink *notificationDiagnosticSink) TryReport(diagnostic notification.Diagnostic) bool {
	if sink == nil {
		return false
	}
	line, valid := notificationDiagnosticLine(diagnostic)
	if !valid || !sink.mutex.TryRLock() {
		return false
	}
	defer sink.mutex.RUnlock()
	if !sink.accepting {
		return false
	}

	select {
	case sink.queue <- line:
		return true
	default:
		return false
	}
}

func (sink *notificationDiagnosticSink) Shutdown(ctx context.Context) error {
	if sink == nil {
		return nil
	}
	sink.stop()

	select {
	case <-sink.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("drain notification diagnostics: %w", ctx.Err())
	}
}

func (sink *notificationDiagnosticSink) stop() {
	sink.stopOnce.Do(func() {
		sink.mutex.Lock()
		sink.accepting = false
		close(sink.queue)
		sink.mutex.Unlock()
	})
}

func (sink *notificationDiagnosticSink) run() {
	defer close(sink.done)
	for line := range sink.queue {
		writeNotificationDiagnostic(sink.writer, line)
	}
}

func writeNotificationDiagnostic(writer io.Writer, line string) {
	defer func() {
		_ = recover()
	}()
	_, _ = io.WriteString(writer, line)
}

func notificationDiagnosticLine(diagnostic notification.Diagnostic) (string, bool) {
	target, valid := notificationDiagnosticTarget(diagnostic.Target)
	if !valid {
		return "", false
	}
	event, valid := notificationDiagnosticEvent(diagnostic.Event)
	if !valid {
		return "", false
	}
	code, message, valid := notificationDiagnosticCode(diagnostic.Code)
	if !valid {
		return "", false
	}

	return "maniud notification: target=" + target + " event=" + event +
		" code=" + code + ": " + message + "\n", true
}

func notificationDiagnosticTarget(target notification.Target) (string, bool) {
	switch target {
	case notification.TargetBark:
		return "bark", true
	case notification.TargetTelegram:
		return "telegram", true
	default:
		return "", false
	}
}

func notificationDiagnosticEvent(event application.EventKind) (string, bool) {
	if event == "" {
		return "unknown", true
	}
	if !notification.SupportsEvent(event) {
		return "", false
	}

	return string(event), true
}

func notificationDiagnosticCode(code notification.DiagnosticCode) (string, string, bool) {
	switch code {
	case notification.DiagnosticSemanticFailed:
		return "semantic_failed", "notification service rejected the request", true
	case notification.DiagnosticTransportFailed:
		return "transport_failed", "notification delivery failed", true
	case notification.DiagnosticInvalidEvent:
		return "invalid_event", "notification event was invalid", true
	case notification.DiagnosticQueueFull:
		return "dropped_queue_full", "notification queue was full", true
	case notification.DiagnosticShutdown:
		return "dropped_shutdown", "notification delivery stopped during shutdown", true
	default:
		return "", "", false
	}
}
