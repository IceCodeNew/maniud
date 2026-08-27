package notification

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
)

type dispatcherSender struct {
	send   func(context.Context, string, string) error
	close  func()
	closed atomic.Bool
}

func (sender *dispatcherSender) Send(ctx context.Context, title, body string) error {
	return sender.send(ctx, title, body)
}

func (sender *dispatcherSender) CloseIdleConnections() {
	sender.closed.Store(true)
	if sender.close != nil {
		sender.close()
	}
}

type diagnosticSinkFunc func(Diagnostic) bool

func (report diagnosticSinkFunc) TryReport(diagnostic Diagnostic) bool {
	return report(diagnostic)
}

func TestDispatcherDoesNotStartWithoutTargets(t *testing.T) {
	t.Parallel()

	dispatcher := NewDispatcher(nil, nil, nil)
	if dispatcher != nil || dispatcher.TryPublish(application.Event{Kind: application.EventPlanPrepared}) ||
		dispatcher.Shutdown(t.Context()) != nil || dispatcher.DroppedEvents() != 0 {
		t.Fatalf("disabled dispatcher = %#v", dispatcher)
	}
	if got := newDispatcherWith([]targetSpec{{target: TargetBark}}, nil); got != nil {
		t.Fatalf("nil sender dispatcher = %#v", got)
	}
}

func TestDispatcherDropsContendedAdmissionForPublicTargets(t *testing.T) {
	t.Parallel()

	dispatcher := NewDispatcher(&HTTPSender{}, &HTTPSender{}, nil)
	if dispatcher == nil {
		t.Fatal("NewDispatcher() = nil")
	}
	dispatcher.mutex.Lock()
	if dispatcher.TryPublish(application.Event{Kind: application.EventPlanPrepared}) {
		t.Fatal("contended dispatcher admitted an event")
	}
	dispatcher.mutex.Unlock()
	if dispatcher.TryPublish(application.Event{}) {
		t.Fatal("invalid event was admitted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := dispatcher.Shutdown(ctx); !errors.Is(err, ErrShutdown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown(cancelled) error = %v", err)
	}
	waitDispatcherSignal(t, dispatcher.done, "public target dispatcher did not stop")
	if dispatcher.DroppedEvents() != 4 {
		t.Fatalf("DroppedEvents() = %d, want 4", dispatcher.DroppedEvents())
	}
}

func TestDispatcherDeliversToBothTargetsAndDrains(t *testing.T) {
	t.Parallel()

	var mutex sync.Mutex
	deliveries := make(map[Target][]notificationMessage)
	sender := func(target Target) *dispatcherSender {
		return &dispatcherSender{send: func(_ context.Context, title, body string) error {
			mutex.Lock()
			deliveries[target] = append(deliveries[target], notificationMessage{title: title, body: body})
			mutex.Unlock()

			return nil
		}}
	}
	bark := sender(TargetBark)
	telegram := sender(TargetTelegram)
	dispatcher := newDispatcherWith([]targetSpec{
		{target: TargetBark, sender: bark},
		{target: TargetTelegram, sender: telegram},
	}, nil)
	event := application.Event{Kind: application.EventPlanPrepared, Project: "example", Service: "api"}
	if !dispatcher.TryPublish(event) {
		t.Fatal("dual-target event was dropped")
	}
	if err := dispatcher.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	mutex.Lock()
	defer mutex.Unlock()
	for _, target := range []Target{TargetBark, TargetTelegram} {
		if len(deliveries[target]) != 1 || deliveries[target][0].title != planPreparedNotificationTitle {
			t.Fatalf("%s deliveries = %#v", target, deliveries[target])
		}
	}
	if !bark.closed.Load() || !telegram.closed.Load() {
		t.Fatalf("sender close state = %t, %t", bark.closed.Load(), telegram.closed.Load())
	}
}

func TestDispatcherIsolatesFullTargetQueue(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	bark := blockingDispatcherSender(entered, release)
	telegramDelivered := make(chan struct{}, notificationQueueDepth+2)
	var telegramDeliveries atomic.Uint64
	telegram := &dispatcherSender{send: func(context.Context, string, string) error {
		telegramDeliveries.Add(1)
		telegramDelivered <- struct{}{}

		return nil
	}}
	var diagnosticsMutex sync.Mutex
	var diagnostics []Diagnostic
	dispatcher := newDispatcherWith([]targetSpec{
		{target: TargetBark, sender: bark},
		{target: TargetTelegram, sender: telegram},
	}, diagnosticSinkFunc(func(diagnostic Diagnostic) bool {
		diagnosticsMutex.Lock()
		diagnostics = append(diagnostics, diagnostic)
		diagnosticsMutex.Unlock()

		return true
	}))
	event := application.Event{Kind: application.EventPlanPrepared}
	if !dispatcher.TryPublish(event) {
		t.Fatal("initial event was dropped")
	}
	waitDispatcherSignal(t, entered, "blocked target did not start delivery")
	waitDispatcherSignal(t, telegramDelivered, "isolated target did not deliver initial event")
	for range notificationQueueDepth {
		if !dispatcher.TryPublish(event) {
			t.Fatal("target queue filled early")
		}
		waitDispatcherSignal(t, telegramDelivered, "isolated target did not keep delivering")
	}
	if dispatcher.TryPublish(event) {
		t.Fatal("full target queue reported complete acceptance")
	}
	waitDispatcherSignal(t, telegramDelivered, "isolated target did not deliver overflow event")
	close(release)
	if err := dispatcher.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	if dispatcher.DroppedEvents() != 1 || telegramDeliveries.Load() != notificationQueueDepth+2 {
		t.Fatalf(
			"isolated queue drops/deliveries = %d / %d",
			dispatcher.DroppedEvents(),
			telegramDeliveries.Load(),
		)
	}
	assertQueueIsolationDiagnostic(t, &diagnosticsMutex, diagnostics, event.Kind)
}

func assertQueueIsolationDiagnostic(
	t *testing.T,
	diagnosticsMutex *sync.Mutex,
	diagnostics []Diagnostic,
	event application.EventKind,
) {
	t.Helper()
	diagnosticsMutex.Lock()
	defer diagnosticsMutex.Unlock()
	if len(diagnostics) != 1 || diagnostics[0] != (Diagnostic{
		Target: TargetBark, Event: event, Code: DiagnosticQueueFull,
	}) {
		t.Fatalf("queue diagnostics = %#v", diagnostics)
	}
}

func TestDispatcherClassifiesDeliveryResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		code        DiagnosticCode
		wantDropped uint64
	}{
		{name: "semantic", err: ErrRejected, code: DiagnosticSemanticFailed},
		{name: "transport", err: ErrDelivery, code: DiagnosticTransportFailed},
		{name: "invalid", err: ErrInvalidMessage, code: DiagnosticInvalidEvent, wantDropped: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var diagnostic Diagnostic
			dispatcher := newDispatcherWith([]targetSpec{{
				target: TargetBark,
				sender: &dispatcherSender{send: func(context.Context, string, string) error {
					return test.err
				}},
			}}, diagnosticSinkFunc(func(value Diagnostic) bool {
				diagnostic = value

				return true
			}))
			event := application.Event{Kind: application.EventPlanPrepared}
			if !dispatcher.TryPublish(event) {
				t.Fatal("delivery result event was dropped")
			}
			if err := dispatcher.Shutdown(t.Context()); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
			if dispatcher.DroppedEvents() != test.wantDropped || diagnostic != (Diagnostic{
				Target: TargetBark, Event: event.Kind, Code: test.code,
			}) {
				t.Fatalf("delivery result = %d drops, %#v", dispatcher.DroppedEvents(), diagnostic)
			}
		})
	}
}

func TestDispatcherBoundsShutdownAndCancelsDelivery(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	sender := cancellingDispatcherSender(entered)
	var diagnostics atomic.Uint64
	dispatcher := newDispatcherWith([]targetSpec{{target: TargetBark, sender: sender}}, diagnosticSinkFunc(
		func(Diagnostic) bool {
			diagnostics.Add(1)

			return true
		},
	))
	if dispatcher == nil {
		t.Fatal("newDispatcherWith() = nil")
	}
	event := application.Event{Kind: application.EventPlanPrepared}
	for range 3 {
		if !dispatcher.TryPublish(event) {
			t.Fatal("shutdown fixture queue dropped early")
		}
	}
	waitDispatcherSignal(t, entered, "shutdown fixture did not start delivery")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := dispatcher.Shutdown(ctx)
	if !errors.Is(err, ErrShutdown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown(cancelled) error = %v", err)
	}
	waitDispatcherSignal(t, dispatcher.done, "cancelled dispatcher did not quiesce")
	if dispatcher.DroppedEvents() != 3 || diagnostics.Load() != 3 || !sender.closed.Load() {
		t.Fatalf(
			"cancelled dispatcher drops = %d, diagnostics %d, closed %t",
			dispatcher.DroppedEvents(), diagnostics.Load(), sender.closed.Load(),
		)
	}
}

func TestDispatcherDropsInvalidAndPostShutdownEvents(t *testing.T) {
	t.Parallel()

	sender := &dispatcherSender{send: func(context.Context, string, string) error { return nil }}
	var diagnostics [2]Diagnostic
	diagnosticCount := 0
	dispatcher := newDispatcherWith([]targetSpec{{target: TargetBark, sender: sender}}, diagnosticSinkFunc(
		func(diagnostic Diagnostic) bool {
			diagnostics[diagnosticCount] = diagnostic
			diagnosticCount++

			return true
		},
	))
	if dispatcher.TryPublish(application.Event{}) {
		t.Fatal("invalid event was accepted")
	}
	if diagnostics[0].Event != "" {
		t.Fatalf("invalid event diagnostic exposed kind %q", diagnostics[0].Event)
	}
	if err := dispatcher.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if dispatcher.TryPublish(application.Event{Kind: application.EventPlanPrepared}) {
		t.Fatal("post-shutdown event was accepted")
	}
	if dispatcher.DroppedEvents() != 2 || diagnosticCount != len(diagnostics) ||
		diagnostics[0].Code != DiagnosticInvalidEvent ||
		diagnostics[1].Code != DiagnosticShutdown {
		t.Fatalf("dropped event count/diagnostics = %d / %#v", dispatcher.DroppedEvents(), diagnostics)
	}
}

func TestDispatcherContainsDiagnosticSinkFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sink DiagnosticSink
	}{
		{name: "drop", sink: diagnosticSinkFunc(func(Diagnostic) bool { return false })},
		{name: "panic", sink: diagnosticSinkFunc(func(Diagnostic) bool { panic("diagnostic sink failure") })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var sends atomic.Uint64
			dispatcher := newDispatcherWith([]targetSpec{{
				target: TargetBark,
				sender: &dispatcherSender{send: func(context.Context, string, string) error {
					sends.Add(1)

					return ErrDelivery
				}},
			}}, test.sink)
			if !dispatcher.TryPublish(application.Event{Kind: application.EventPlanPrepared}) {
				t.Fatal("diagnostic failure changed event acceptance")
			}
			if err := dispatcher.Shutdown(t.Context()); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
			if sends.Load() != 1 {
				t.Fatalf("diagnostic failure sends = %d", sends.Load())
			}
		})
	}
}

func TestDispatcherContainsSenderPanics(t *testing.T) {
	t.Parallel()

	sender := &dispatcherSender{
		send:  func(context.Context, string, string) error { panic("send failure") },
		close: func() { panic("close failure") },
	}
	var diagnostic Diagnostic
	dispatcher := newDispatcherWith([]targetSpec{{target: TargetBark, sender: sender}}, diagnosticSinkFunc(
		func(value Diagnostic) bool {
			diagnostic = value

			return true
		},
	))
	event := application.Event{Kind: application.EventPlanPrepared}
	if !dispatcher.TryPublish(event) {
		t.Fatal("sender panic fixture was dropped")
	}
	if err := dispatcher.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if !sender.closed.Load() || diagnostic != (Diagnostic{
		Target: TargetBark, Event: event.Kind, Code: DiagnosticTransportFailed,
	}) {
		t.Fatalf("sender panic result = closed %t, diagnostic %#v", sender.closed.Load(), diagnostic)
	}
}

func TestDispatcherShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	dispatcher := newDispatcherWith([]targetSpec{{
		target: TargetBark,
		sender: &dispatcherSender{send: func(context.Context, string, string) error { return nil }},
	}}, nil)
	if dispatcher == nil {
		t.Fatal("newDispatcherWith() = nil")
	}
	if err := dispatcher.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitDispatcherSignal(t, dispatcher.done, "dispatcher did not stop")
	if err := dispatcher.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown(second) error = %v", err)
	}
}

func waitDispatcherSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func blockingDispatcherSender(entered chan struct{}, release <-chan struct{}) *dispatcherSender {
	return &dispatcherSender{send: func(context.Context, string, string) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release

		return nil
	}}
}

func cancellingDispatcherSender(entered chan struct{}) *dispatcherSender {
	return &dispatcherSender{send: func(ctx context.Context, _, _ string) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-ctx.Done()

		return ctx.Err()
	}}
}
