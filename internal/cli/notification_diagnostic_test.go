package cli

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/notification"
)

var errDiagnosticWriterFailure = errors.New("diagnostic writer failure")

//nolint:funlen // The table covers every permitted target, event, and diagnostic code.
func TestNotificationDiagnosticSinkWritesBoundedValueFreeLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		diagnostic notification.Diagnostic
		want       string
	}{
		{
			name: "semantic bark",
			diagnostic: notification.Diagnostic{
				Target: notification.TargetBark,
				Event:  application.EventPlanPrepared,
				Code:   notification.DiagnosticSemanticFailed,
			},
			want: "maniud notification: target=bark event=plan_prepared code=semantic_failed: " +
				"notification service rejected the request\n",
		},
		{
			name: "transport telegram",
			diagnostic: notification.Diagnostic{
				Target: notification.TargetTelegram,
				Event:  application.EventRuntimeEffectStarted,
				Code:   notification.DiagnosticTransportFailed,
			},
			want: "maniud notification: target=telegram event=runtime_effect_started code=transport_failed: " +
				"notification delivery failed\n",
		},
		{
			name: "invalid unknown event",
			diagnostic: notification.Diagnostic{
				Target: notification.TargetBark,
				Code:   notification.DiagnosticInvalidEvent,
			},
			want: "maniud notification: target=bark event=unknown code=invalid_event: " +
				"notification event was invalid\n",
		},
		{
			name: "queue full",
			diagnostic: notification.Diagnostic{
				Target: notification.TargetTelegram,
				Event:  application.EventTransactionSucceeded,
				Code:   notification.DiagnosticQueueFull,
			},
			want: "maniud notification: target=telegram event=transaction_succeeded code=dropped_queue_full: " +
				"notification queue was full\n",
		},
		{
			name: "shutdown",
			diagnostic: notification.Diagnostic{
				Target: notification.TargetBark,
				Event:  application.EventTransactionRestored,
				Code:   notification.DiagnosticShutdown,
			},
			want: "maniud notification: target=bark event=transaction_restored code=dropped_shutdown: " +
				"notification delivery stopped during shutdown\n",
		},
		{
			name: "failed event",
			diagnostic: notification.Diagnostic{
				Target: notification.TargetTelegram,
				Event:  application.EventTransactionFailed,
				Code:   notification.DiagnosticTransportFailed,
			},
			want: "maniud notification: target=telegram event=transaction_failed code=transport_failed: " +
				"notification delivery failed\n",
		},
		{
			name: "GitOps service failure",
			diagnostic: notification.Diagnostic{
				Target: notification.TargetBark,
				Event:  application.EventGitOpsServiceApplyFailed,
				Code:   notification.DiagnosticQueueFull,
			},
			want: "maniud notification: target=bark event=gitops_service_apply_failed code=dropped_queue_full: " +
				"notification queue was full\n",
		},
		{
			name: "daemon unavailable",
			diagnostic: notification.Diagnostic{
				Target: notification.TargetTelegram,
				Event:  application.EventDaemonUnavailable,
				Code:   notification.DiagnosticShutdown,
			},
			want: "maniud notification: target=telegram event=daemon_unavailable code=dropped_shutdown: " +
				"notification delivery stopped during shutdown\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			sink := newNotificationDiagnosticSink(&output)
			if !sink.TryReport(test.diagnostic) {
				t.Fatal("TryReport() rejected a valid diagnostic")
			}
			if err := sink.Shutdown(t.Context()); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("diagnostic output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNotificationDiagnosticSinkRejectsUntrustedFields(t *testing.T) {
	t.Parallel()

	const untrusted = "credential-value"
	tests := []notification.Diagnostic{
		{Target: untrusted, Event: application.EventPlanPrepared, Code: notification.DiagnosticQueueFull},
		{Target: notification.TargetBark, Event: untrusted, Code: notification.DiagnosticQueueFull},
		{Target: notification.TargetBark, Event: application.EventPlanPrepared, Code: untrusted},
		{
			Target: notification.TargetBark,
			Event:  application.EventActionIntentRecorded,
			Code:   notification.DiagnosticQueueFull,
		},
		{
			Target: notification.TargetBark,
			Event:  application.EventPostconditionObserved,
			Code:   notification.DiagnosticQueueFull,
		},
		{
			Target: notification.TargetBark,
			Event:  application.EventActionCompleted,
			Code:   notification.DiagnosticQueueFull,
		},
		{
			Target: notification.TargetBark,
			Event:  application.EventTransactionDegraded,
			Code:   notification.DiagnosticQueueFull,
		},
	}
	var output bytes.Buffer
	sink := newNotificationDiagnosticSink(&output)
	for _, diagnostic := range tests {
		if sink.TryReport(diagnostic) {
			t.Fatalf("TryReport(%#v) accepted an untrusted field", diagnostic)
		}
	}
	if err := sink.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("untrusted diagnostic output = %q", output.String())
	}
}

func TestNotificationDiagnosticSinkIsNonblockingAndBoundsShutdown(t *testing.T) {
	t.Parallel()

	writer := newBlockingDiagnosticWriter()
	sink := newNotificationDiagnosticSink(writer)
	diagnostic := notification.Diagnostic{
		Target: notification.TargetBark,
		Event:  application.EventPlanPrepared,
		Code:   notification.DiagnosticQueueFull,
	}
	if !sink.TryReport(diagnostic) {
		t.Fatal("TryReport() rejected the initial diagnostic")
	}
	<-writer.entered
	for range notificationDiagnosticQueueDepth {
		if !sink.TryReport(diagnostic) {
			t.Fatal("TryReport() filled the queue early")
		}
	}
	if sink.TryReport(diagnostic) {
		t.Fatal("TryReport() accepted a diagnostic after the queue filled")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sink.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown(cancelled) error = %v", err)
	}
	close(writer.release)
	if err := sink.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown(retry) error = %v", err)
	}
	if writer.writes.Load() != notificationDiagnosticQueueDepth+1 {
		t.Fatalf("diagnostic writes = %d", writer.writes.Load())
	}
	if sink.TryReport(diagnostic) {
		t.Fatal("TryReport() accepted a diagnostic after shutdown")
	}
}

func TestNotificationDiagnosticSinkContainsWriterFailuresAndAdmissionContention(t *testing.T) {
	t.Parallel()

	writer := &failingDiagnosticWriter{}
	sink := newNotificationDiagnosticSink(writer)
	sink.mutex.Lock()
	if sink.TryReport(notification.Diagnostic{
		Target: notification.TargetBark,
		Event:  application.EventPlanPrepared,
		Code:   notification.DiagnosticTransportFailed,
	}) {
		t.Fatal("TryReport() blocked on contended admission")
	}
	sink.mutex.Unlock()
	for range 2 {
		if !sink.TryReport(notification.Diagnostic{
			Target: notification.TargetBark,
			Event:  application.EventPlanPrepared,
			Code:   notification.DiagnosticTransportFailed,
		}) {
			t.Fatal("TryReport() rejected a writer failure fixture")
		}
	}
	if err := sink.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if writer.writes.Load() != 2 {
		t.Fatalf("writer attempts = %d", writer.writes.Load())
	}

	var nilSink *notificationDiagnosticSink
	if nilSink.TryReport(notification.Diagnostic{}) || nilSink.Shutdown(t.Context()) != nil {
		t.Fatal("nil notification diagnostic sink was not inert")
	}
	if got := newNotificationDiagnosticSink(nil); got != nil {
		t.Fatalf("nil writer sink = %#v", got)
	}
}

type blockingDiagnosticWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	writes  atomic.Uint64
}

func newBlockingDiagnosticWriter() *blockingDiagnosticWriter {
	return &blockingDiagnosticWriter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (writer *blockingDiagnosticWriter) Write(payload []byte) (int, error) {
	writer.once.Do(func() {
		close(writer.entered)
		<-writer.release
	})
	writer.writes.Add(1)

	return len(payload), nil
}

type failingDiagnosticWriter struct {
	writes atomic.Uint64
}

func (writer *failingDiagnosticWriter) Write(_ []byte) (int, error) {
	if writer.writes.Add(1) == 1 {
		panic("diagnostic writer failure")
	}

	return 0, errDiagnosticWriterFailure
}
