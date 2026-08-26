package notification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	notifyhttp "github.com/nikoksr/notify/service/http"
)

const (
	contractBarkURL             = "https://api.day.app/push"
	contractTelegramToken       = "telegram-test-token"
	contractTelegramURL         = "https://api.telegram.org/bot" + contractTelegramToken + "/sendMessage"
	maximumContractResponseSize = 64
)

var (
	errContractDelivery         = errors.New("notification delivery failed")
	errContractInvalidResponse  = errors.New("notification response is invalid")
	errContractRedirectRejected = errors.New("notification redirect rejected")
)

type contractRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip contractRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type contractResponseBody struct {
	reader io.Reader
	closed atomic.Bool
}

func (body *contractResponseBody) Read(content []byte) (int, error) {
	return body.reader.Read(content) //nolint:wrapcheck // The test body preserves the wrapped reader contract.
}

func (body *contractResponseBody) Close() error {
	body.closed.Store(true)

	return nil
}

type contractContextKey struct{}

//nolint:cyclop // The cancellation test checks each bounded join point explicitly.
func TestNotifyGenericHTTPPropagatesInflightCancellation(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	exited := make(chan struct{})
	var calls atomic.Int64
	transport := contractRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.String() != contractBarkURL || request.Context().Value(contractContextKey{}) != "caller" {
			return nil, errContractInvalidResponse
		}
		close(entered)
		<-request.Context().Done()
		close(exited)

		return nil, request.Context().Err()
	})
	service := notifyContractService(contractBarkURL, transport, nil)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contractContextKey{}, "caller"))
	result := make(chan error, 1)
	go func() {
		result <- sendNotifyContract(ctx, service)
	}()

	select {
	case <-entered:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("notify transport did not receive the request")
	}

	select {
	case err := <-result:
		if !errors.Is(err, errContractDelivery) {
			t.Fatalf("sendNotifyContract() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("notify send did not return after cancellation")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("notify transport did not exit after cancellation")
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
}

func TestNotifyGenericHTTPUsesInjectedRedirectPolicy(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	transport := contractRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)

		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://127.0.0.1/private"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    request,
		}, nil
	})
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errContractRedirectRejected
		},
	}
	service := notifyContractServiceWithClient(contractTelegramURL, client, nil)

	err := sendNotifyContract(t.Context(), service)
	if err == nil {
		t.Fatal("sendNotifyContract(redirect) = nil")
	}
	if !errors.Is(err, errContractDelivery) || calls.Load() != 1 {
		t.Fatalf("sendNotifyContract(redirect) = %v, calls %d", err, calls.Load())
	}
	if strings.Contains(err.Error(), contractTelegramToken) || strings.Contains(err.Error(), contractTelegramURL) {
		t.Fatalf("redacted error contains credential or URL: %q", err)
	}
}

func TestNotifyGenericHTTPSupportsBoundedSemanticResponseValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		url       string
		response  string
		validate  func(*http.Response) error
		wantError bool
	}{
		{name: "Bark success", url: contractBarkURL, response: `{"code":200}`, validate: validateContractBarkResponse},
		{
			name: "Bark semantic failure", url: contractBarkURL, response: `{"code":400}`,
			validate: validateContractBarkResponse, wantError: true,
		},
		{
			name: "Telegram success", url: contractTelegramURL, response: `{"ok":true}`,
			validate: validateContractTelegramResponse,
		},
		{
			name: "Telegram semantic failure", url: contractTelegramURL, response: `{"ok":false}`,
			validate: validateContractTelegramResponse, wantError: true,
		},
		{
			name: "bounded response", url: contractBarkURL,
			response: strings.Repeat("x", maximumContractResponseSize+1),
			validate: validateContractBarkResponse, wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			body := &contractResponseBody{reader: strings.NewReader(test.response)}
			var calls atomic.Int64
			transport := contractRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)

				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       body,
					Request:    request,
				}, nil
			})
			service := notifyContractService(test.url, transport, test.validate)

			err := sendNotifyContract(t.Context(), service)
			if test.wantError != errors.Is(err, errContractDelivery) {
				t.Fatalf("sendNotifyContract() error = %v, want failure %t", err, test.wantError)
			}
			if calls.Load() != 1 || !body.closed.Load() {
				t.Fatalf("transport calls = %d, response closed = %t", calls.Load(), body.closed.Load())
			}
			if err != nil && (strings.Contains(err.Error(), contractTelegramToken) ||
				strings.Contains(err.Error(), test.response)) {
				t.Fatalf("redacted error contains credential or response: %q", err)
			}
		})
	}
}

func notifyContractService(
	url string,
	transport http.RoundTripper,
	validate func(*http.Response) error,
) *notifyhttp.Service {
	return notifyContractServiceWithClient(url, &http.Client{Transport: transport}, validate)
}

func notifyContractServiceWithClient(
	url string,
	client *http.Client,
	validate func(*http.Response) error,
) *notifyhttp.Service {
	service := notifyhttp.New()
	service.WithClient(client)
	service.AddReceivers(&notifyhttp.Webhook{
		ContentType: "application/json",
		Header:      make(http.Header),
		Method:      http.MethodPost,
		URL:         url,
		BuildPayload: func(subject, message string) any {
			return map[string]string{"subject": subject, "message": message}
		},
	})
	if validate != nil {
		service.PostSend(func(_ *http.Request, response *http.Response) error {
			return validate(response)
		})
	}

	return service
}

func sendNotifyContract(ctx context.Context, service *notifyhttp.Service) error {
	if err := service.Send(ctx, "subject", "message"); err != nil {
		return errContractDelivery
	}

	return nil
}

func validateContractBarkResponse(response *http.Response) error {
	var payload struct {
		Code int `json:"code"`
	}
	if err := decodeContractResponse(response, &payload); err != nil || payload.Code != http.StatusOK {
		return errContractInvalidResponse
	}

	return nil
}

func validateContractTelegramResponse(response *http.Response) error {
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := decodeContractResponse(response, &payload); err != nil || !payload.OK {
		return errContractInvalidResponse
	}

	return nil
}

func decodeContractResponse(response *http.Response, payload any) error {
	content, err := io.ReadAll(io.LimitReader(response.Body, maximumContractResponseSize+1))
	if err != nil || len(content) > maximumContractResponseSize || json.Unmarshal(content, payload) != nil {
		return errContractInvalidResponse
	}

	return nil
}
