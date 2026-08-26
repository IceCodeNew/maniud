package notification

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	testBarkDeviceKey     = "bark-device-key"
	testBarkEncryptionKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testTelegramToken     = "123456:telegram_test-token"
	testTelegramChat      = "-100123456"
	testTitle             = "maniud upgrade succeeded"
	testBody              = "example/api reached the requested state"
)

type senderRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip senderRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type senderSecretError string

func (err senderSecretError) Error() string {
	return string(err)
}

const errSenderTransport senderSecretError = "request to https://api.telegram.org/bot" +
	testTelegramToken + "/sendMessage failed"

func TestProductionSenderConstructorsUseFixedTargets(t *testing.T) {
	t.Parallel()

	bark, barkErr := NewBarkSender(testBarkDeviceKey, "")
	telegram, telegramErr := NewTelegramSender(testTelegramToken, testTelegramChat)
	if barkErr != nil || telegramErr != nil || bark == nil || telegram == nil {
		t.Fatalf("production senders = %v, %v, %v, %v", bark, barkErr, telegram, telegramErr)
	}
	bark.CloseIdleConnections()
	telegram.CloseIdleConnections()
}

func TestBarkSenderBuildsOfficialPayload(t *testing.T) {
	t.Parallel()

	var requestBody []byte
	client := senderTestClient(t, barkURL, `{"code":200,"message":"success"}`, &requestBody)
	sender, err := newBarkSenderWith(testBarkDeviceKey, "", client)
	if err != nil {
		t.Fatalf("newBarkSenderWith() error = %v", err)
	}
	if err = sender.Send(t.Context(), testTitle, testBody); err != nil {
		t.Fatalf("Bark Send() error = %v", err)
	}

	var got barkPlainPayload
	if err = json.Unmarshal(requestBody, &got); err != nil {
		t.Fatalf("decode Bark payload: %v", err)
	}
	if got.DeviceKey != testBarkDeviceKey {
		t.Errorf("Bark device key = %q, want %q", got.DeviceKey, testBarkDeviceKey)
	}
	if got.Title != testTitle {
		t.Errorf("Bark title = %q, want %q", got.Title, testTitle)
	}
	if got.Body != testBody {
		t.Errorf("Bark body = %q, want %q", got.Body, testBody)
	}

	sender.CloseIdleConnections()
}

func TestBarkSenderEncryptsPayloadWithFreshIV(t *testing.T) {
	t.Parallel()

	requestBodies := make([][]byte, 0, 2)
	client := encryptedBarkTestClient(t, &requestBodies)
	sender, err := newBarkSenderWith(
		testBarkDeviceKey,
		testBarkEncryptionKey,
		client,
	)
	if err != nil {
		t.Fatalf("new encrypted Bark sender: %v", err)
	}
	if err = sender.Send(t.Context(), testTitle, testBody); err != nil {
		t.Fatalf("first encrypted Bark Send() error = %v", err)
	}
	if err = sender.Send(t.Context(), testTitle, testBody); err != nil {
		t.Fatalf("second encrypted Bark Send() error = %v", err)
	}

	first := decodeBarkRequest(t, requestBodies[0])
	second := decodeBarkRequest(t, requestBodies[1])
	if first.IV == second.IV || first.Ciphertext == second.Ciphertext {
		t.Fatalf("encrypted Bark requests reused IV or ciphertext: %q, %q", first.IV, second.IV)
	}

	assertBarkContent(t, decryptBarkRequest(t, first))
	assertBarkContent(t, decryptBarkRequest(t, second))
}

func TestBarkSenderMapsEncryptionFailureWithoutValues(t *testing.T) {
	t.Parallel()

	sender := newHTTPSender(http.DefaultClient, barkURL, func(title, body string) any {
		return barkEncryptedPayload{
			deviceKey: testBarkDeviceKey,
			title:     title,
			body:      body,
			key:       []byte(testBarkEncryptionKey),
			random:    senderFailingReader{},
		}
	}, validateBarkResponse)
	err := sender.Send(t.Context(), testTitle, testBody)
	if err == nil {
		t.Fatal("Bark encryption failure = nil")
	}
	if !errors.Is(err, ErrDelivery) {
		t.Fatalf("Bark encryption failure = %q, want ErrDelivery", err)
	}
	message := err.Error()
	if strings.Contains(message, testBarkDeviceKey) || strings.Contains(message, testBarkEncryptionKey) ||
		strings.Contains(message, testBody) {
		t.Fatalf("Bark encryption failure exposed a sensitive value: %q", err)
	}
}

func TestBarkEncryptedPayloadReportsEncodingAndCipherFailures(t *testing.T) {
	t.Parallel()

	invalidText := string([]byte{0xff})
	tests := []struct {
		name    string
		payload barkEncryptedPayload
	}{
		{
			name: "plaintext",
			payload: barkEncryptedPayload{
				title: invalidText,
			},
		},
		{
			name: "cipher",
			payload: barkEncryptedPayload{
				title: testTitle, body: testBody, key: []byte("short"),
				random: strings.NewReader(strings.Repeat("x", barkIVRandomBytes)),
			},
		},
		{
			name: "request",
			payload: barkEncryptedPayload{
				deviceKey: invalidText, title: testTitle, body: testBody,
				key:    []byte(testBarkEncryptionKey),
				random: strings.NewReader(strings.Repeat("x", barkIVRandomBytes)),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := test.payload.MarshalJSON(); err == nil {
				t.Fatal("MarshalJSON() unexpectedly succeeded")
			}
		})
	}
	if _, err := newBarkCipherWith(
		[]byte(testBarkEncryptionKey),
		func(cipher.Block) (cipher.AEAD, error) { return nil, errTestTransport },
	); !errors.Is(err, errTestTransport) {
		t.Fatalf("newBarkCipherWith(GCM failure) = %v", err)
	}
}

func TestTelegramSenderBuildsOfficialPayload(t *testing.T) {
	t.Parallel()

	url := "https://" + telegramHost + "/bot" + testTelegramToken + "/sendMessage"
	var requestBody []byte
	client := senderTestClient(t, url, `{"ok":true,"result":{"message_id":1}}`, &requestBody)
	sender, err := newTelegramSenderWith(testTelegramToken, testTelegramChat, client)
	if err != nil {
		t.Fatalf("newTelegramSenderWith() error = %v", err)
	}
	if err = sender.Send(t.Context(), testTitle, testBody); err != nil {
		t.Fatalf("Telegram Send() error = %v", err)
	}

	var payload struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if json.Unmarshal(requestBody, &payload) != nil || payload.ChatID != testTelegramChat ||
		payload.Text != testTitle+"\n\n"+testBody {
		t.Fatalf("Telegram payload = %s", requestBody)
	}

	sender.CloseIdleConnections()
}

func TestSendersMapResponseFailuresWithoutValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		status   int
		bark     bool
	}{
		{name: "Bark semantic failure", response: `{"code":400,"message":"private Bark body"}`, status: 200, bark: true},
		{name: "Telegram semantic failure", response: `{"ok":false,"description":"private Telegram body"}`, status: 200},
		{name: "malformed", response: `{`, status: 200, bark: true},
		{name: "duplicate", response: `{"code":200,"code":400}`, status: 200, bark: true},
		{name: "oversized", response: strings.Repeat("x", int(maximumNotificationResponse)+1), status: 200},
		{name: "HTTP failure", response: `{"ok":true}`, status: 429},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := senderResponseClient(test.status, test.response)
			var sender *HTTPSender
			var err error
			if test.bark {
				sender, err = newBarkSenderWith(testBarkDeviceKey, "", client)
			} else {
				sender, err = newTelegramSenderWith(testTelegramToken, testTelegramChat, client)
			}
			if err != nil {
				t.Fatalf("new sender error = %v", err)
			}

			err = sender.Send(t.Context(), testTitle, testBody)
			if !errors.Is(err, ErrRejected) || strings.Contains(err.Error(), test.response) ||
				strings.Contains(err.Error(), testBarkDeviceKey) || strings.Contains(err.Error(), testTelegramToken) {
				t.Fatalf("response failure = %q", err)
			}
		})
	}
}

func TestSenderMapsTransportAndCancellationWithoutValues(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: senderRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errSenderTransport
	})}
	sender, err := newTelegramSenderWith(testTelegramToken, testTelegramChat, client)
	if err != nil {
		t.Fatalf("newTelegramSenderWith() error = %v", err)
	}
	err = sender.Send(t.Context(), testTitle, testBody)
	if err == nil {
		t.Fatal("transport delivery error = nil")
	}
	if !errors.Is(err, ErrDelivery) ||
		errors.Is(err, errSenderTransport) || strings.Contains(err.Error(), testTelegramToken) {
		t.Fatalf("transport failure = %q", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	client.Transport = senderRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		cancel()

		return nil, request.Context().Err()
	})
	if err = sender.Send(ctx, testTitle, testBody); !errors.Is(err, ErrDelivery) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled delivery = %v", err)
	}
}

func TestBarkSenderRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"", " key", "key\n", strings.Repeat("x", maximumNotificationCredential+1)} {
		if sender, err := newBarkSenderWith(key, "", http.DefaultClient); sender != nil ||
			!errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("newBarkSenderWith(%q) = %v, %v", key, sender, err)
		}
	}
	for _, key := range []string{
		strings.Repeat("a", 15),
		strings.Repeat("a", 17),
		strings.Repeat("a", 33),
		strings.Repeat("a", 14) + "é",
	} {
		if sender, err := newBarkSenderWith(
			testBarkDeviceKey,
			key,
			http.DefaultClient,
		); sender != nil ||
			!errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), key) {
			t.Fatalf("newBarkSenderWith(encryption key) = %v, %v", sender, err)
		}
	}
	if sender, err := newBarkSenderWith(testBarkDeviceKey, "", nil); sender != nil ||
		!errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("newBarkSenderWith(nil client) = %v, %v", sender, err)
	}
}

func TestBarkSenderAcceptsAESKeySizes(t *testing.T) {
	t.Parallel()

	for _, size := range []int{barkAES128KeyBytes, barkAES192KeyBytes, barkAES256KeyBytes} {
		sender, err := newBarkSenderWith(
			testBarkDeviceKey,
			strings.Repeat("a", size),
			http.DefaultClient,
		)
		if err != nil || sender == nil {
			t.Fatalf("newBarkSenderWith(%d-byte encryption key) = %v, %v", size, sender, err)
		}
	}
}

func TestTelegramSenderRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"", "bad/token", "bad token", "token\n"} {
		if sender, err := newTelegramSenderWith(token, testTelegramChat, http.DefaultClient); sender != nil ||
			!errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("newTelegramSenderWith(%q) = %v, %v", token, sender, err)
		}
	}
	for _, chatID := range []string{"", " chat", "chat\n"} {
		if sender, err := newTelegramSenderWith(testTelegramToken, chatID, http.DefaultClient); sender != nil ||
			!errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("newTelegramSenderWith(chat %q) = %v, %v", chatID, sender, err)
		}
	}
	if sender, err := newTelegramSenderWith(testTelegramToken, testTelegramChat, nil); sender != nil ||
		!errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("newTelegramSenderWith(nil client) = %v, %v", sender, err)
	}
}

func TestSenderRejectsInvalidMessages(t *testing.T) {
	t.Parallel()

	valid, err := newBarkSenderWith(testBarkDeviceKey, "", senderResponseClient(200, `{"code":200}`))
	if err != nil {
		t.Fatalf("new valid sender error = %v", err)
	}
	invalidMessages := [][2]string{
		{"", testBody},
		{testTitle, ""},
		{"bad\nname", testBody},
		{testTitle, "bad\x00body"},
		{strings.Repeat("x", maximumNotificationTitle+1), testBody},
		{testTitle, strings.Repeat("x", maximumNotificationBody+1)},
		{string([]byte{0xff}), testBody},
		{testTitle, string([]byte{0xff})},
	}
	for _, message := range invalidMessages {
		if err = valid.Send(t.Context(), message[0], message[1]); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("Send(%q, %q) = %v", message[0], message[1], err)
		}
	}
	var missing *HTTPSender
	if err = missing.Send(t.Context(), testTitle, testBody); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("nil sender Send() = %v", err)
	}
	missing.CloseIdleConnections()
	(&HTTPSender{}).CloseIdleConnections()
}

func TestResponseValidatorsRejectMissingBodyAndReadFailure(t *testing.T) {
	t.Parallel()

	if validateBarkResponse(nil) == nil ||
		validateTelegramResponse(&http.Response{StatusCode: http.StatusOK}) == nil {
		t.Fatal("response validator accepted missing response body")
	}
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(senderFailingReader{})}
	if validateBarkResponse(response) == nil {
		t.Fatal("Bark validator accepted unreadable response")
	}
	if validateTelegramResponse(&http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true,"ok":false}`)),
	}) == nil {
		t.Fatal("Telegram validator accepted duplicate response fields")
	}
}

type senderFailingReader struct{}

func (senderFailingReader) Read([]byte) (int, error) {
	return 0, errTestTransport
}

func senderTestClient(t *testing.T, wantURL, response string, requestBody *[]byte) *http.Client {
	t.Helper()

	return &http.Client{Transport: senderRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != wantURL ||
			request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("notification request = %s %s, headers %#v", request.Method, request.URL, request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read notification request: %v", err)
		}
		*requestBody = body

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    request,
		}, nil
	})}
}

func senderResponseClient(status int, response string) *http.Client {
	return &http.Client{Transport: senderRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    request,
		}, nil
	})}
}

func encryptedBarkTestClient(t *testing.T, requestBodies *[][]byte) *http.Client {
	t.Helper()

	return &http.Client{Transport: senderRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read encrypted Bark request: %v", err)
		}
		*requestBodies = append(*requestBodies, body)

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":200}`)),
			Request:    request,
		}, nil
	})}
}

func decodeBarkRequest(t *testing.T, body []byte) barkEncryptedRequest {
	t.Helper()

	var request barkEncryptedRequest
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode encrypted Bark request: %v", err)
	}

	return request
}

func decryptBarkRequest(t *testing.T, request barkEncryptedRequest) barkEncryptedContent {
	t.Helper()

	if request.DeviceKey != testBarkDeviceKey || len(request.IV) != aes.BlockSize-4 {
		t.Fatalf("encrypted Bark request metadata = %#v", request)
	}
	decoded, err := base64.StdEncoding.DecodeString(request.Ciphertext)
	if err != nil {
		t.Fatalf("decode Bark ciphertext: %v", err)
	}
	block, err := aes.NewCipher([]byte(testBarkEncryptionKey))
	if err != nil {
		t.Fatalf("create Bark test cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("create Bark test GCM: %v", err)
	}
	plaintext, err := gcm.Open(nil, []byte(request.IV), decoded, nil)
	if err != nil {
		t.Fatalf("decrypt Bark ciphertext: %v", err)
	}
	var content barkEncryptedContent
	if err = json.Unmarshal(plaintext, &content); err != nil {
		t.Fatalf("decode Bark plaintext: %v", err)
	}

	return content
}

func assertBarkContent(t *testing.T, content barkEncryptedContent) {
	t.Helper()

	if content.Title != testTitle {
		t.Errorf("encrypted Bark title = %q, want %q", content.Title, testTitle)
	}
	if content.Body != testBody {
		t.Errorf("encrypted Bark body = %q, want %q", content.Body, testBody)
	}
	if content.Sound != barkSound {
		t.Errorf("encrypted Bark sound = %q, want %q", content.Sound, barkSound)
	}
}

func TestSenderCloseIdleConnectionsUsesClient(t *testing.T) {
	t.Parallel()

	var closed atomic.Bool
	client := &http.Client{Transport: closeIdleRoundTripper{closed: &closed}}
	sender := &HTTPSender{client: client}
	sender.CloseIdleConnections()
	if !closed.Load() {
		t.Fatal("sender did not close idle connections")
	}
}

type closeIdleRoundTripper struct {
	closed *atomic.Bool
}

func (closeIdleRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errTestTransport
}

func (transport closeIdleRoundTripper) CloseIdleConnections() {
	transport.closed.Store(true)
}
