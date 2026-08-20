package docker

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

const (
	testArchitecture            = "amd64"
	testOS                      = "linux"
	testProduct                 = "29.7.2"
	testUnsupportedArchitecture = "s390x"
)

type nestedJSONValue struct {
	Value int `json:"value"`
}

type nestedJSONDocument struct {
	Nested []nestedJSONValue `json:"nested"`
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testClient(roundTripper http.RoundTripper) *Client {
	var emptyVersion Version

	return &Client{
		httpClient: &http.Client{
			Transport:     roundTripper,
			CheckRedirect: nil,
			Jar:           nil,
			Timeout:       requestTimeout,
		},
		baseURL: url.URL{ //nolint:exhaustruct // A request base intentionally omits URL resource fields.
			Scheme: httpScheme,
			Host:   dummyDockerHost,
		},
		version: emptyVersion,
	}
}

type engineFixture struct {
	maximum       string
	pingStatus    int
	pingBody      string
	versionStatus int
	contentType   string
	version       string
	onRequest     func(*http.Request)
}

func validEngineFixture(maximum string) engineFixture {
	return engineFixture{
		maximum:       maximum,
		pingStatus:    0,
		pingBody:      "OK",
		versionStatus: 0,
		contentType:   jsonContentType,
		version:       versionDocument(maximum, "1.40", testProduct, testOS, testArchitecture),
		onRequest:     nil,
	}
}

func engineHandler(t *testing.T, fixture engineFixture) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if fixture.onRequest != nil {
			fixture.onRequest(request)
		}

		if request.URL.Path == "/_ping" {
			if fixture.maximum != "" {
				response.Header().Set(apiVersionHeader, fixture.maximum)
			}

			status := fixture.pingStatus
			if status == 0 || request.Method == http.MethodGet {
				status = http.StatusOK
			}

			response.WriteHeader(status)

			if request.Method == http.MethodGet {
				_, _ = io.WriteString(response, fixture.pingBody)
			}

			return
		}

		status := fixture.versionStatus
		if status == 0 {
			status = http.StatusOK
		}

		contentType := fixture.contentType
		if contentType == "" {
			contentType = jsonContentType
		}

		response.Header().Set("Content-Type", contentType)
		response.WriteHeader(status)
		_, _ = io.WriteString(response, fixture.version)
	})
}

func versionDocument(maximum, minimum, product, osName, architecture string) string {
	return "{" + strconv.Quote("Version") + ":" + strconv.Quote(product) +
		"," + strconv.Quote("ApiVersion") + ":" + strconv.Quote(maximum) +
		"," + strconv.Quote("MinAPIVersion") + ":" + strconv.Quote(minimum) +
		"," + strconv.Quote("Os") + ":" + strconv.Quote(osName) +
		"," + strconv.Quote("Arch") + ":" + strconv.Quote(architecture) + "}"
}

func testVPNEndpoint(t *testing.T, serverURL string, warningSink WarningSink) Endpoint {
	t.Helper()

	address := "tcp://" + strings.TrimPrefix(serverURL, "http://")

	endpoint, err := VPNEndpoint(address, warningSink)
	if err != nil {
		t.Fatalf("VPNEndpoint() error = %v", err)
	}

	return endpoint
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", value, err)
	}

	return parsed
}

func quoteJSON(value string) string {
	return strconv.Quote(value)
}
