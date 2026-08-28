package notification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/jsonstrict"
	notifyhttp "github.com/nikoksr/notify/service/http"
)

const (
	barkHost                      = "api.day.app"
	barkURL                       = "https://" + barkHost + "/push"
	telegramHost                  = "api.telegram.org"
	telegramTokenCharacters       = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_:-"
	maximumNotificationTitle      = 256
	maximumNotificationBody       = 3 << 10
	maximumNotificationResponse   = int64(16 << 10)
	maximumNotificationCredential = 4 << 10
)

var (
	// ErrInvalidMessage reports a message that cannot be represented within the
	// fixed Bark and Telegram payload bounds.
	ErrInvalidMessage = errors.New("notification message is invalid")
	// ErrDelivery reports a transport or library failure without exposing its
	// URL, credential, or third-party response.
	ErrDelivery = errors.New("notification delivery failed")
	// ErrRejected reports a bounded response that did not prove delivery.
	ErrRejected = errors.New("notification was rejected")
	// ErrInvalidConfiguration reports an invalid target credential without
	// including its value.
	ErrInvalidConfiguration = errors.New("notification configuration is invalid")

	errInvalidResponse = errors.New("notification response is invalid")
)

// HTTPSender delivers pre-rendered notifications through one fixed official
// target. Its errors never expose target credentials, URLs, or response bodies.
type HTTPSender struct {
	service *notifyhttp.Service
	client  *http.Client
}

// NewBarkSender creates a sender for Bark's official public API. An empty
// encryption key sends the plaintext payload. Each encrypted payload carries
// a fresh random IV.
func NewBarkSender(deviceKey, encryptionKey string) (*HTTPSender, error) {
	return newBarkSenderWith(deviceKey, encryptionKey, newNotificationHTTPClient(barkHost))
}

func newBarkSenderWith(deviceKey, encryptionKey string, client *http.Client) (*HTTPSender, error) {
	if !validNotificationCredential(deviceKey) || !validBarkEncryptionKey(encryptionKey) || client == nil {
		return nil, ErrInvalidConfiguration
	}

	return newHTTPSender(client, barkURL, func(title, body string) any {
		return newBarkPayload(deviceKey, encryptionKey, title, body)
	}, validateBarkResponse), nil
}

// NewTelegramSender creates a sender for Telegram's official Bot API.
func NewTelegramSender(token, chatID string) (*HTTPSender, error) {
	return newTelegramSenderWith(token, chatID, newNotificationHTTPClient(telegramHost))
}

func newTelegramSenderWith(token, chatID string, client *http.Client) (*HTTPSender, error) {
	if !validTelegramToken(token) || !validNotificationCredential(chatID) || client == nil {
		return nil, ErrInvalidConfiguration
	}

	url := "https://" + telegramHost + "/bot" + token + "/sendMessage"

	return newHTTPSender(client, url, func(title, body string) any {
		return struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}{ChatID: chatID, Text: title + "\n\n" + body}
	}, validateTelegramResponse), nil
}

func newHTTPSender(
	client *http.Client,
	url string,
	buildPayload notifyhttp.BuildPayloadFn,
	validate func(*http.Response) error,
) *HTTPSender {
	service := notifyhttp.New()
	service.WithClient(client)
	service.AddReceivers(&notifyhttp.Webhook{
		ContentType:  "application/json",
		Header:       make(http.Header),
		Method:       http.MethodPost,
		URL:          url,
		BuildPayload: buildPayload,
	})
	service.PostSend(func(_ *http.Request, response *http.Response) error {
		return validate(response)
	})

	return &HTTPSender{service: service, client: client}
}

// Send delivers one bounded title and body using the caller's context.
func (sender *HTTPSender) Send(ctx context.Context, title, body string) error {
	if sender == nil || sender.service == nil || ctx == nil || !validNotificationMessage(title, body) {
		return ErrInvalidMessage
	}

	err := sender.service.Send(ctx, title, body)
	if err == nil {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return errors.Join(ErrDelivery, contextErr)
	}
	if errors.Is(err, errInvalidResponse) {
		return ErrRejected
	}

	return ErrDelivery
}

// CloseIdleConnections releases reusable transport connections.
func (sender *HTTPSender) CloseIdleConnections() {
	if sender != nil && sender.client != nil {
		sender.client.CloseIdleConnections()
	}
}

func validateBarkResponse(response *http.Response) error {
	var payload struct {
		Code int `json:"code"`
	}
	if !decodeNotificationResponse(response, &payload) || payload.Code != http.StatusOK {
		return errInvalidResponse
	}

	return nil
}

func validateTelegramResponse(response *http.Response) error {
	var payload struct {
		OK bool `json:"ok"`
	}
	if !decodeNotificationResponse(response, &payload) || !payload.OK {
		return errInvalidResponse
	}

	return nil
}

func decodeNotificationResponse(response *http.Response, target any) bool {
	if response == nil || response.Body == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return false
	}

	value, valid := jsonstrict.Read(response.Body, maximumNotificationResponse)

	return valid && json.Unmarshal(value, target) == nil
}

func validNotificationMessage(title, body string) bool {
	return validNotificationText(title, maximumNotificationTitle, false) &&
		validNotificationText(body, maximumNotificationBody, true)
}

func validNotificationCredential(value string) bool {
	return validNotificationText(value, maximumNotificationCredential, false) &&
		strings.TrimSpace(value) == value
}

func validTelegramToken(value string) bool {
	if !validNotificationCredential(value) {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune(telegramTokenCharacters, character) {
			return false
		}
	}

	return true
}

func validNotificationText(value string, maximum int, multiline bool) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}

	return multiline || !strings.ContainsAny(value, "\r\n")
}
