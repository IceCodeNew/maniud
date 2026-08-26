package cli

import (
	"context"
	"errors"
	"io"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/notification"
)

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
