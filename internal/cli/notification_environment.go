package cli

import "github.com/IceCodeNew/maniud/internal/notification"

const (
	barkDeviceKeyEnvironment     = "BARK_DEVICE_KEY"
	barkEncryptionKeyEnvironment = "BARK_ENCRYPTION_KEY"
	//nolint:gosec // This is the public environment variable name, not a credential.
	telegramBotTokenEnvironment = "TELEGRAM_BOT_TOKEN"
	telegramChatIDEnvironment   = "TELEGRAM_CHAT_ID"

	incompleteBarkConfigurationMessage = "Bark notification configuration is incomplete: " +
		barkEncryptionKeyEnvironment + " requires " + barkDeviceKeyEnvironment +
		"; set the device key or unset the encryption key"
	incompleteTelegramConfigurationMessage = "Telegram notification configuration is incomplete: " +
		telegramBotTokenEnvironment + " and " + telegramChatIDEnvironment +
		" must be set together; set the missing variable or unset both to disable Telegram"
	invalidBarkConfigurationMessage = "Bark notification configuration is invalid: " +
		barkDeviceKeyEnvironment + " must contain a valid bounded device key and " +
		barkEncryptionKeyEnvironment + " must contain 16, 24, or 32 ASCII characters when set"
	invalidTelegramConfigurationMessage = "Telegram notification configuration is invalid: " +
		telegramBotTokenEnvironment + " and " + telegramChatIDEnvironment +
		" must contain valid bounded values"
)

var (
	errIncompleteBarkConfiguration     = notificationConfigurationError(incompleteBarkConfigurationMessage)
	errIncompleteTelegramConfiguration = notificationConfigurationError(incompleteTelegramConfigurationMessage)
	errInvalidBarkConfiguration        = notificationConfigurationError(invalidBarkConfigurationMessage)
	errInvalidTelegramConfiguration    = notificationConfigurationError(invalidTelegramConfigurationMessage)
)

type notificationConfigurationError string

func (configurationError notificationConfigurationError) Error() string {
	return string(configurationError)
}

type notificationConfiguration struct {
	barkDeviceKey     string
	barkEncryptionKey string
	telegramBotToken  string
	telegramChatID    string
}

func readNotificationConfiguration(environment map[string]string) (notificationConfiguration, error) {
	configuration := notificationConfiguration{
		barkDeviceKey:     environment[barkDeviceKeyEnvironment],
		barkEncryptionKey: environment[barkEncryptionKeyEnvironment],
		telegramBotToken:  environment[telegramBotTokenEnvironment],
		telegramChatID:    environment[telegramChatIDEnvironment],
	}
	if configuration.barkEncryptionKey != "" && configuration.barkDeviceKey == "" {
		return notificationConfiguration{}, errIncompleteBarkConfiguration
	}
	if (configuration.telegramBotToken == "") != (configuration.telegramChatID == "") {
		return notificationConfiguration{}, errIncompleteTelegramConfiguration
	}

	return configuration, nil
}

func openNotificationDispatcher(
	configuration notificationConfiguration,
	diagnostics notification.DiagnosticSink,
) (*notification.Dispatcher, error) {
	var bark *notification.HTTPSender
	var telegram *notification.HTTPSender
	var err error
	if configuration.barkDeviceKey != "" {
		bark, err = notification.NewBarkSender(configuration.barkDeviceKey, configuration.barkEncryptionKey)
		if err != nil {
			return nil, errInvalidBarkConfiguration
		}
	}
	if configuration.telegramBotToken != "" {
		telegram, err = notification.NewTelegramSender(
			configuration.telegramBotToken,
			configuration.telegramChatID,
		)
		if err != nil {
			if bark != nil {
				bark.CloseIdleConnections()
			}

			return nil, errInvalidTelegramConfiguration
		}
	}

	return notification.NewDispatcher(bark, telegram, diagnostics), nil
}
