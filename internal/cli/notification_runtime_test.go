package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
)

var errUnrelatedNotificationFailure = errors.New("unrelated notification failure")

func TestOpenProcessNotificationsKeepsDisabledConfigurationInert(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	notifications, err := openProcessNotifications(nil, &stderr)
	if err != nil || notifications.dispatcher != nil || stderr.Len() != 0 {
		t.Fatalf("openProcessNotifications(disabled) = %#v, %v, %q", notifications, err, stderr.String())
	}

	var disabled *processNotifications
	if disabled.TryPublish(application.Event{}) {
		t.Fatal("nil process notifications published an event")
	}
	if disabled.DroppedEvents() != 0 {
		t.Fatalf("nil process notifications dropped events = %d", disabled.DroppedEvents())
	}
	disabled.Shutdown(t.Context())
	writeNotificationConfigurationFailure(&stderr, errUnrelatedNotificationFailure)
	if stderr.Len() != 0 {
		t.Fatalf("unrelated configuration output = %q", stderr.String())
	}
}

func TestOpenProcessNotificationsReportsConfigurationFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		environment map[string]string
		want        error
		secret      string
	}{
		{
			name:        "partial Telegram",
			environment: map[string]string{telegramBotTokenEnvironment: testTelegramBotToken},
			want:        errIncompleteTelegramConfiguration,
		},
		{
			name:        "invalid Bark",
			environment: map[string]string{barkDeviceKeyEnvironment: "secret\n"},
			want:        errInvalidBarkConfiguration,
			secret:      "secret",
		},
		{
			name: "invalid Telegram",
			environment: map[string]string{
				telegramBotTokenEnvironment: testInvalidTelegram, telegramChatIDEnvironment: testTelegramChatID,
			},
			want:   errInvalidTelegramConfiguration,
			secret: testInvalidTelegram,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			notifications, err := openProcessNotifications(test.environment, &stderr)
			if notifications.dispatcher != nil || !errors.Is(err, test.want) ||
				stderr.String() != test.want.Error()+"\n" ||
				test.secret != "" && bytes.Contains(stderr.Bytes(), []byte(test.secret)) {
				t.Fatalf("openProcessNotifications() = %#v, %v, %q", notifications, err, stderr.String())
			}
		})
	}
}

func TestProcessNotificationsFiltersInternalEventsAndDrainsDiagnostics(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	notifications, err := openProcessNotifications(
		map[string]string{barkDeviceKeyEnvironment: testBarkDeviceKey},
		&stderr,
	)
	if err != nil || notifications.dispatcher == nil {
		t.Fatalf("openProcessNotifications(Bark) = %#v, %v", notifications, err)
	}
	if (&notifications).TryPublish(application.Event{Kind: application.EventActionCompleted}) {
		t.Fatal("internal event was accepted for notification delivery")
	}
	if notifications.DroppedEvents() != 1 {
		t.Fatalf("notification dropped events = %d, want 1", notifications.DroppedEvents())
	}
	notifications.Shutdown(t.Context())

	want := "maniud notification: target=bark event=unknown code=invalid_event: " +
		"notification event was invalid\n"
	if stderr.String() != want {
		t.Fatalf("notification diagnostic = %q, want %q", stderr.String(), want)
	}
}
