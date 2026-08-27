package notification

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	notificationQueueDepth  = 32
	notificationTargetCount = 2
)

// ErrShutdown reports that notification draining exceeded its caller's budget.
var ErrShutdown = errors.New("notification shutdown deadline exceeded")

// Target identifies one fixed notification service.
type Target string

const (
	// TargetBark selects the fixed Bark delivery service.
	TargetBark Target = "bark"
	// TargetTelegram selects the fixed Telegram delivery service.
	TargetTelegram Target = "telegram"
)

// DiagnosticCode classifies a value-free delivery or queue observation.
type DiagnosticCode string

const (
	// DiagnosticSemanticFailed identifies a remote semantic rejection.
	DiagnosticSemanticFailed DiagnosticCode = "semantic_failed"
	// DiagnosticTransportFailed identifies a transport delivery failure.
	DiagnosticTransportFailed DiagnosticCode = "transport_failed"
	// DiagnosticInvalidEvent identifies an event that cannot be rendered or sent.
	DiagnosticInvalidEvent DiagnosticCode = "invalid_event"
	// DiagnosticQueueFull identifies a full target queue drop.
	DiagnosticQueueFull DiagnosticCode = "dropped_queue_full"
	// DiagnosticShutdown identifies a shutdown admission or drain drop.
	DiagnosticShutdown DiagnosticCode = "dropped_shutdown"
)

// Diagnostic is a bounded observation that never contains configuration or
// third-party values.
type Diagnostic struct {
	Target Target
	Event  application.EventKind
	Code   DiagnosticCode
}

// DiagnosticSink accepts one process-local notification observation without
// waiting for external I/O.
type DiagnosticSink interface {
	TryReport(diagnostic Diagnostic) bool
}

// Dispatcher fans application events out to isolated bounded target workers.
type Dispatcher struct {
	mutex       sync.RWMutex
	accepting   bool
	targets     []*targetDispatcher
	diagnostics DiagnosticSink
	cancel      context.CancelFunc
	done        chan struct{}
	stopOnce    sync.Once
}

type targetSender interface {
	Send(ctx context.Context, title, body string) error
	CloseIdleConnections()
}

type targetSpec struct {
	target Target
	sender targetSender
}

type queuedNotification struct {
	event   application.EventKind
	message notificationMessage
}

type targetDispatcher struct {
	target   Target
	sender   targetSender
	queue    chan queuedNotification
	counters targetCounters
}

type targetCounters struct {
	droppedInvalid   atomic.Uint64
	droppedQueueFull atomic.Uint64
	droppedShutdown  atomic.Uint64
}

// NewDispatcher starts one worker for each configured official target. It
// returns nil without starting goroutines when both targets are absent.
func NewDispatcher(bark, telegram *HTTPSender, diagnostics DiagnosticSink) *Dispatcher {
	specifications := make([]targetSpec, 0, notificationTargetCount)
	if bark != nil {
		specifications = append(specifications, targetSpec{target: TargetBark, sender: bark})
	}
	if telegram != nil {
		specifications = append(specifications, targetSpec{target: TargetTelegram, sender: telegram})
	}

	return newDispatcherWith(specifications, diagnostics)
}

func newDispatcherWith(specifications []targetSpec, diagnostics DiagnosticSink) *Dispatcher {
	targets := make([]*targetDispatcher, 0, len(specifications))
	for _, specification := range specifications {
		if specification.sender != nil {
			targets = append(targets, &targetDispatcher{
				target: specification.target,
				sender: specification.sender,
				queue:  make(chan queuedNotification, notificationQueueDepth),
			})
		}
	}
	if len(targets) == 0 {
		return nil
	}

	deliveryContext, cancel := context.WithCancel(context.Background())
	dispatcher := &Dispatcher{
		accepting: true, targets: targets, diagnostics: diagnostics,
		cancel: cancel, done: make(chan struct{}),
	}
	var workers sync.WaitGroup
	for _, target := range targets {
		workers.Go(func() {
			target.run(deliveryContext, dispatcher)
		})
	}
	go func() {
		workers.Wait()
		cancel()
		close(dispatcher.done)
	}()

	return dispatcher
}

// TryPublish renders and queues one event without waiting for delivery.
func (dispatcher *Dispatcher) TryPublish(event application.Event) bool {
	if dispatcher == nil {
		return false
	}
	message, valid := renderApplicationEvent(event)
	if !valid {
		dispatcher.dropAll("", DiagnosticInvalidEvent, func(target *targetDispatcher) {
			target.counters.droppedInvalid.Add(1)
		})

		return false
	}
	if !dispatcher.mutex.TryRLock() {
		dispatcher.dropShutdown(event.Kind)

		return false
	}
	defer dispatcher.mutex.RUnlock()
	if !dispatcher.accepting {
		dispatcher.dropShutdown(event.Kind)

		return false
	}

	accepted := true
	queued := queuedNotification{event: event.Kind, message: message}
	for _, target := range dispatcher.targets {
		select {
		case target.queue <- queued:
		default:
			accepted = false
			target.counters.droppedQueueFull.Add(1)
			dispatcher.report(Diagnostic{Target: target.target, Event: event.Kind, Code: DiagnosticQueueFull})
		}
	}

	return accepted
}

// Shutdown stops admission and drains queued deliveries until the non-nil ctx expires.
func (dispatcher *Dispatcher) Shutdown(ctx context.Context) error {
	if dispatcher == nil {
		return nil
	}
	dispatcher.stop()

	select {
	case <-dispatcher.done:
		return nil
	case <-ctx.Done():
		dispatcher.cancel()

		return errors.Join(ErrShutdown, ctx.Err())
	}
}

// DroppedEvents returns the number of target deliveries discarded before or
// during shutdown. One application event may contribute once per target.
func (dispatcher *Dispatcher) DroppedEvents() uint64 {
	if dispatcher == nil {
		return 0
	}

	var dropped uint64
	for _, target := range dispatcher.targets {
		dropped += target.counters.droppedInvalid.Load() +
			target.counters.droppedQueueFull.Load() +
			target.counters.droppedShutdown.Load()
	}

	return dropped
}

func (dispatcher *Dispatcher) stop() {
	dispatcher.stopOnce.Do(func() {
		dispatcher.mutex.Lock()
		dispatcher.accepting = false
		for _, target := range dispatcher.targets {
			close(target.queue)
		}
		dispatcher.mutex.Unlock()
	})
}

func (dispatcher *Dispatcher) dropShutdown(event application.EventKind) {
	dispatcher.dropAll(event, DiagnosticShutdown, func(target *targetDispatcher) {
		target.counters.droppedShutdown.Add(1)
	})
}

func (dispatcher *Dispatcher) dropAll(
	event application.EventKind,
	code DiagnosticCode,
	increment func(*targetDispatcher),
) {
	for _, target := range dispatcher.targets {
		increment(target)
		dispatcher.report(Diagnostic{Target: target.target, Event: event, Code: code})
	}
}

func (dispatcher *Dispatcher) report(diagnostic Diagnostic) {
	if dispatcher.diagnostics == nil {
		return
	}

	defer func() {
		_ = recover()
	}()
	_ = dispatcher.diagnostics.TryReport(diagnostic)
}

func (target *targetDispatcher) run(ctx context.Context, dispatcher *Dispatcher) {
	defer closeTargetSender(target.sender)
	for queued := range target.queue {
		if ctx.Err() != nil {
			target.dropShutdown(dispatcher, queued.event)

			continue
		}

		requestContext, cancel := context.WithTimeout(ctx, notificationRequestTimeout)
		err := sendNotification(requestContext, target.sender, queued.message)
		cancel()
		target.recordResult(ctx, dispatcher, queued.event, err)
	}
}

func sendNotification(ctx context.Context, sender targetSender, message notificationMessage) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrDelivery
		}
	}()

	err = sender.Send(ctx, message.title, message.body)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrRejected):
		return ErrRejected
	case errors.Is(err, ErrInvalidMessage):
		return ErrInvalidMessage
	default:
		return ErrDelivery
	}
}

func closeTargetSender(sender targetSender) {
	defer func() {
		_ = recover()
	}()
	sender.CloseIdleConnections()
}

func (target *targetDispatcher) recordResult(
	ctx context.Context,
	dispatcher *Dispatcher,
	event application.EventKind,
	err error,
) {
	switch {
	case err == nil:
		return
	case ctx.Err() != nil:
		target.dropShutdown(dispatcher, event)
	case errors.Is(err, ErrRejected):
		dispatcher.report(Diagnostic{Target: target.target, Event: event, Code: DiagnosticSemanticFailed})
	case errors.Is(err, ErrInvalidMessage):
		target.counters.droppedInvalid.Add(1)
		dispatcher.report(Diagnostic{Target: target.target, Event: event, Code: DiagnosticInvalidEvent})
	default:
		dispatcher.report(Diagnostic{Target: target.target, Event: event, Code: DiagnosticTransportFailed})
	}
}

func (target *targetDispatcher) dropShutdown(dispatcher *Dispatcher, event application.EventKind) {
	target.counters.droppedShutdown.Add(1)
	dispatcher.report(Diagnostic{Target: target.target, Event: event, Code: DiagnosticShutdown})
}
