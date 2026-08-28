package cli

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/notification"
)

const notificationDrainTimeout = 5 * time.Second

type processNotifications struct {
	dispatcher  *notification.Dispatcher
	diagnostics *notificationDiagnosticSink
}

func openProcessNotifications(
	environment map[string]string,
	stderr io.Writer,
) (processNotifications, error) {
	configuration, err := readNotificationConfiguration(environment)
	if err != nil {
		writeNotificationConfigurationFailure(stderr, err)

		return processNotifications{}, err
	}
	if configuration == (notificationConfiguration{}) {
		return processNotifications{}, nil
	}

	diagnostics := newNotificationDiagnosticSink(stderr)
	dispatcher, err := openNotificationDispatcher(configuration, diagnostics)
	if err != nil {
		_ = diagnostics.Shutdown(context.Background())
		writeNotificationConfigurationFailure(stderr, err)

		return processNotifications{}, err
	}

	return processNotifications{dispatcher: dispatcher, diagnostics: diagnostics}, nil
}

//nolint:ireturn // The process-scoped notifier exposes only the application event capability.
func (notifications *processNotifications) Open(
	environment map[string]string,
	stderr io.Writer,
) (application.EventSink, error) {
	var err error
	*notifications, err = openProcessNotifications(environment, stderr)
	if err != nil || notifications.dispatcher == nil {
		return nil, err
	}

	return notifications, nil
}

func (notifications *processNotifications) Close() {
	if notifications.dispatcher == nil {
		return
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), notificationDrainTimeout)
	defer cancel()
	notifications.Shutdown(shutdownContext)
}

func (notifications *processNotifications) TryPublish(event application.Event) bool {
	if notifications == nil || notifications.dispatcher == nil {
		return false
	}

	return notifications.dispatcher.TryPublish(event)
}

func (notifications *processNotifications) DroppedEvents() uint64 {
	if notifications == nil || notifications.dispatcher == nil {
		return 0
	}

	return notifications.dispatcher.DroppedEvents()
}

func (notifications *processNotifications) Shutdown(ctx context.Context) {
	if notifications == nil || notifications.dispatcher == nil {
		return
	}

	_ = notifications.dispatcher.Shutdown(ctx)
	_ = notifications.diagnostics.Shutdown(ctx)
}

func writeNotificationConfigurationFailure(stderr io.Writer, err error) {
	if configurationError, ok := errors.AsType[notificationConfigurationError](err); ok {
		writeNotificationDiagnostic(stderr, configurationError.Error()+"\n")
	}
}
