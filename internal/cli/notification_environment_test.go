package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/notification"
)

const (
	testBarkDeviceKey     = "device"
	testBarkEncryptionKey = "aaaaaaaaaaaaaaaa"
	testTelegramBotToken  = "123:token"
	testTelegramChatID    = "456"
	testInvalidTelegram   = "secret/value"
)

func TestReadNotificationConfigurationTruthTable(t *testing.T) {
	t.Parallel()

	for _, test := range notificationConfigurationTruthTable() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assertNotificationConfiguration(t, test)
		})
	}
}

func assertNotificationConfiguration(t *testing.T, test notificationConfigurationTest) {
	t.Helper()

	configuration, err := readNotificationConfiguration(test.environment)
	if test.wantErr != nil {
		if !errors.Is(err, test.wantErr) || configuration != (notificationConfiguration{}) {
			t.Fatalf("readNotificationConfiguration() error = %v", err)
		}

		return
	}
	if err != nil {
		t.Fatalf("readNotificationConfiguration() error = %v", err)
	}

	got := notificationConfigurationEnablement{
		bark:           configuration.barkDeviceKey != "",
		barkEncryption: configuration.barkEncryptionKey != "",
		telegram:       configuration.telegramBotToken != "" && configuration.telegramChatID != "",
	}
	want := notificationConfigurationEnablement{
		bark:           test.wantBark,
		barkEncryption: test.wantBarkEncryption,
		telegram:       test.wantTelegram,
	}
	if got != want {
		t.Fatalf("readNotificationConfiguration() enablement = %+v, want %+v", got, want)
	}
}

type notificationConfigurationEnablement struct {
	bark           bool
	barkEncryption bool
	telegram       bool
}

type notificationConfigurationTest struct {
	name               string
	environment        map[string]string
	wantBark           bool
	wantBarkEncryption bool
	wantTelegram       bool
	wantErr            error
}

func notificationConfigurationTruthTable() []notificationConfigurationTest {
	return slices.Concat(validNotificationConfigurations(), invalidNotificationConfigurations())
}

func validNotificationConfigurations() []notificationConfigurationTest {
	return []notificationConfigurationTest{
		{name: "disabled"},
		{
			name: "Bark only", environment: map[string]string{barkDeviceKeyEnvironment: testBarkDeviceKey},
			wantBark: true,
		},
		{
			name: "encrypted Bark",
			environment: map[string]string{
				barkDeviceKeyEnvironment:     testBarkDeviceKey,
				barkEncryptionKeyEnvironment: testBarkEncryptionKey,
			},
			wantBark: true, wantBarkEncryption: true,
		},
		{
			name: "Telegram only",
			environment: map[string]string{
				telegramBotTokenEnvironment: testTelegramBotToken, telegramChatIDEnvironment: testTelegramChatID,
			},
			wantTelegram: true,
		},
		{
			name: "both",
			environment: map[string]string{
				barkDeviceKeyEnvironment:     testBarkDeviceKey,
				barkEncryptionKeyEnvironment: testBarkEncryptionKey,
				telegramBotTokenEnvironment:  testTelegramBotToken,
				telegramChatIDEnvironment:    testTelegramChatID,
			},
			wantBark: true, wantBarkEncryption: true, wantTelegram: true,
		},
	}
}

func invalidNotificationConfigurations() []notificationConfigurationTest {
	return []notificationConfigurationTest{
		{
			name:        "missing Bark device key",
			environment: map[string]string{barkEncryptionKeyEnvironment: testBarkEncryptionKey},
			wantErr:     errIncompleteBarkConfiguration,
		},
		{
			name:        "missing Telegram chat",
			environment: map[string]string{telegramBotTokenEnvironment: testTelegramBotToken},
			wantErr:     errIncompleteTelegramConfiguration,
		},
		{
			name:        "missing Telegram token",
			environment: map[string]string{telegramChatIDEnvironment: testTelegramChatID},
			wantErr:     errIncompleteTelegramConfiguration,
		},
	}
}

func TestOpenNotificationDispatcherValidatesAndStartsConfiguredTargets(t *testing.T) {
	t.Parallel()

	for _, test := range notificationDispatcherConfigurations() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dispatcher, err := openNotificationDispatcher(test.configuration, nil)
			if test.wantErr != nil {
				assertNotificationDispatcherError(t, dispatcher, err, test.wantErr, test.secret)

				return
			}
			assertNotificationDispatcherStarted(t, dispatcher, err, test)
		})
	}
}

type notificationDispatcherTest struct {
	name          string
	configuration notificationConfiguration
	wantEnabled   bool
	wantErr       error
	secret        string
}

func notificationDispatcherConfigurations() []notificationDispatcherTest {
	return []notificationDispatcherTest{
		{name: "disabled"},
		{
			name: "Bark", configuration: notificationConfiguration{barkDeviceKey: testBarkDeviceKey},
			wantEnabled: true,
		},
		{
			name: "Telegram",
			configuration: notificationConfiguration{
				telegramBotToken: testTelegramBotToken, telegramChatID: testTelegramChatID,
			},
			wantEnabled: true,
		},
		{
			name: "both",
			configuration: notificationConfiguration{
				barkDeviceKey: testBarkDeviceKey, barkEncryptionKey: testBarkEncryptionKey,
				telegramBotToken: testTelegramBotToken,
				telegramChatID:   testTelegramChatID,
			},
			wantEnabled: true,
		},
		{
			name: "invalid Bark", configuration: notificationConfiguration{barkDeviceKey: " secret\n"},
			wantErr: errInvalidBarkConfiguration, secret: "secret",
		},
		{
			name: "invalid Bark encryption",
			configuration: notificationConfiguration{
				barkDeviceKey: testBarkDeviceKey, barkEncryptionKey: "short-secret",
			},
			wantErr: errInvalidBarkConfiguration, secret: "short-secret",
		},
		{
			name: "invalid Telegram after Bark",
			configuration: notificationConfiguration{
				barkDeviceKey: testBarkDeviceKey, telegramBotToken: testInvalidTelegram,
				telegramChatID: testTelegramChatID,
			},
			wantErr: errInvalidTelegramConfiguration, secret: testInvalidTelegram,
		},
		{
			name: "invalid Telegram only",
			configuration: notificationConfiguration{
				telegramBotToken: "other/secret", telegramChatID: testTelegramChatID,
			},
			wantErr: errInvalidTelegramConfiguration, secret: "other/secret",
		},
	}
}

func assertNotificationDispatcherError(
	t *testing.T,
	dispatcher *notification.Dispatcher,
	err, want error,
	secret string,
) {
	t.Helper()
	if !errors.Is(err, want) || dispatcher != nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("openNotificationDispatcher() error = %v", err)
	}
}

func assertNotificationDispatcherStarted(
	t *testing.T,
	dispatcher *notification.Dispatcher,
	err error,
	test notificationDispatcherTest,
) {
	t.Helper()
	if err != nil || (dispatcher != nil) != test.wantEnabled {
		t.Fatalf("openNotificationDispatcher() enabled = %t, error %v", dispatcher != nil, err)
	}
	if dispatcher == nil {
		return
	}
	if err := dispatcher.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if test.configuration.barkDeviceKey != "" &&
		dispatcher.Stats(notification.TargetBark) != (notification.TargetStats{}) {
		t.Fatalf("Bark startup stats = %#v", dispatcher.Stats(notification.TargetBark))
	}
}
