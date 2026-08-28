package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type EmbeddedProtocolText string

type embeddedProtocolDocument struct {
	Value int `json:"value"`
}

type compatibleProtocolDocument struct {
	*embeddedProtocolDocument
	EmbeddedProtocolText

	Ignored bool `json:"-"`
}

//nolint:funlen // The table is the strict negotiation rejection corpus.
func TestConnectRejectsProtocolViolations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*engineFixture)
	}{
		{
			name: "ping failure",
			mutate: func(fixture *engineFixture) {
				fixture.pingStatus = http.StatusInternalServerError
			},
		},
		{
			name: "missing API header",
			mutate: func(fixture *engineFixture) {
				fixture.maximum = ""
			},
		},
		{
			name: "invalid API header",
			mutate: func(fixture *engineFixture) {
				fixture.maximum = "latest"
			},
		},
		{
			name: "old API",
			mutate: func(fixture *engineFixture) {
				fixture.maximum = testUnsupportedAPIVersion
			},
		},
		{
			name: "version failure",
			mutate: func(fixture *engineFixture) {
				fixture.versionStatus = http.StatusInternalServerError
			},
		},
		{
			name: "version content type",
			mutate: func(fixture *engineFixture) {
				fixture.contentType = "text/plain"
			},
		},
		{
			name: "unknown version field",
			mutate: func(fixture *engineFixture) {
				fixture.version = strings.TrimSuffix(fixture.version, "}") +
					"," + strconv.Quote("Unknown") + ":true}"
			},
		},
		{
			name: "trailing version value",
			mutate: func(fixture *engineFixture) {
				fixture.version += `{}`
			},
		},
		{
			name: "oversized version",
			mutate: func(fixture *engineFixture) {
				fixture.version = versionDocument(
					maximumAPIVersion, "1.40", strings.Repeat("x", maximumJSONBytes), "linux", "amd64",
				)
			},
		},
		{
			name: "invalid minimum",
			mutate: func(fixture *engineFixture) {
				fixture.version = versionDocument(maximumAPIVersion, "old", "29.7.2", "linux", "amd64")
			},
		},
		{
			name: "invalid maximum",
			mutate: func(fixture *engineFixture) {
				fixture.version = versionDocument("new", "1.40", "29.7.2", "linux", "amd64")
			},
		},
		{
			name: "maximum drift",
			mutate: func(fixture *engineFixture) {
				fixture.version = versionDocument("1.56", "1.40", "29.7.2", "linux", "amd64")
			},
		},
		{
			name: "minimum above selected",
			mutate: func(fixture *engineFixture) {
				fixture.version = versionDocument(maximumAPIVersion, "1.56", "29.7.2", "linux", "amd64")
			},
		},
		{
			name: "missing product",
			mutate: func(fixture *engineFixture) {
				fixture.version = versionDocument(maximumAPIVersion, "1.40", "", "linux", "amd64")
			},
		},
		{
			name: "missing OS",
			mutate: func(fixture *engineFixture) {
				fixture.version = versionDocument(maximumAPIVersion, "1.40", "29.7.2", "", "amd64")
			},
		},
		{
			name: "missing architecture",
			mutate: func(fixture *engineFixture) {
				fixture.version = versionDocument(maximumAPIVersion, "1.40", "29.7.2", "linux", "")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := validEngineFixture(maximumAPIVersion)
			test.mutate(&fixture)
			server := httptest.NewServer(engineHandler(t, fixture))
			t.Cleanup(server.Close)

			endpoint := testVPNEndpoint(t, server.URL, func(Warning) error { return nil })
			client, version, err := Connect(context.Background(), endpoint)

			var emptyVersion Version
			if client != nil || version != emptyVersion || !errors.Is(err, ErrProtocol) {
				t.Fatalf("Connect() = %#v, %#v, %v; want nil, zero, ErrProtocol", client, version, err)
			}
		})
	}
}

func TestConnectFallsBackToGetPing(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusNotImplemented,
	} {
		fixture := validEngineFixture(minimumAPIVersion)
		fixture.pingStatus = status
		fixture.pingBody = " OK\n"
		fixture.version = versionDocument(minimumAPIVersion, "1.40", "29.4.0", "linux", "amd64")
		server := httptest.NewServer(engineHandler(t, fixture))
		endpoint := testVPNEndpoint(t, server.URL, func(Warning) error { return nil })

		client, _, err := Connect(context.Background(), endpoint)
		server.Close()
		if err != nil {
			t.Fatalf("Connect(HEAD status %d) error = %v", status, err)
		}
		client.CloseIdleConnections()
	}

	for _, invalidBody := range []string{"not ok", strings.Repeat("x", maximumPingBytes+1)} {
		invalidFixture := validEngineFixture(minimumAPIVersion)
		invalidFixture.pingStatus = http.StatusMethodNotAllowed
		invalidFixture.pingBody = invalidBody
		invalidServer := httptest.NewServer(engineHandler(t, invalidFixture))
		endpoint := testVPNEndpoint(t, invalidServer.URL, func(Warning) error { return nil })
		_, _, err := Connect(context.Background(), endpoint)

		invalidServer.Close()

		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("Connect(fallback body) error = %v, want ErrProtocol", err)
		}
	}
}

func TestLateProbeTransportErrors(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32

	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			response := httptest.NewRecorder()
			response.WriteHeader(http.StatusMethodNotAllowed)

			return response.Result(), nil
		}

		return nil, io.ErrUnexpectedEOF
	}))

	_, err := client.ping(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ping(GET transport failure) error = %v, want ErrUnavailable", err)
	}

	client = testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))

	selected, valid := parseAPIVersion(maximumAPIVersion)
	if !valid {
		t.Fatalf("parseAPIVersion(%q) failed", maximumAPIVersion)
	}

	_, err = client.serverVersion(context.Background(), selected, selected)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("serverVersion(transport failure) error = %v, want ErrUnavailable", err)
	}
}

func TestProtocolHelpersContainMalformedInput(t *testing.T) {
	t.Parallel()

	if validPingBody(errorReader{}) {
		t.Fatal("validPingBody(error reader) = true")
	}

	if decodeStrictJSON(errorReader{}, &struct{}{}) {
		t.Fatal("decodeStrictJSON(error reader) = true")
	}

	for _, value := range []string{"{", `{"value":1,`} {
		if decodeStrictJSON(strings.NewReader(value), &struct{}{}) {
			t.Fatalf("decodeStrictJSON(%q) = true", value)
		}
	}

	if isJSON("not a content type") || isJSON("text/plain") || !isJSON("application/json; charset=utf-8") {
		t.Fatal("isJSON() media type handling is invalid")
	}

	client := testClient(http.DefaultTransport)
	client.baseURL.Host = "bad\nhost"

	response, err := client.request(context.Background(), http.MethodGet, "/version")
	if response != nil {
		closeResponse(response)
	}

	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("request(malformed URL) error = %v, want ErrProtocol", err)
	}
}

func TestStrictJSONRejectsNestedDuplicateKeys(t *testing.T) {
	t.Parallel()

	target := nestedJSONDocument{Nested: nil}
	if decodeStrictJSON(strings.NewReader(`{"nested":[{"value":1,"value":1}]}`), &target) {
		t.Fatal("decodeStrictJSON(duplicate nested key) = true")
	}

	if !decodeStrictJSON(strings.NewReader(`{"nested":[{"value":1}]}`), &target) ||
		len(target.Nested) != 1 || target.Nested[0].Value != 1 {
		t.Fatalf("decodeStrictJSON(valid nested value) = %#v", target)
	}
}

func TestCompatibleJSONSchemaBoundaries(t *testing.T) {
	t.Parallel()

	schema := reflect.TypeFor[compatibleProtocolDocument]()
	if decodeCompatibleJSON(strings.NewReader(`[]`), &compatibleProtocolDocument{}, schema) {
		t.Fatal("decodeCompatibleJSON(array) = true")
	}
	if decodeCompatibleJSON(strings.NewReader(`{}`), new(string), reflect.TypeFor[string]()) {
		t.Fatal("decodeCompatibleJSON(non-struct schema) = true")
	}

	if !supportedSchemaField(schema, "value", make(map[reflect.Type]bool)) {
		t.Fatal("supportedSchemaField(embedded pointer field) = false")
	}
	if !supportedSchemaField(schema, "EmbeddedProtocolText", make(map[reflect.Type]bool)) {
		t.Fatal("supportedSchemaField(embedded scalar) = false")
	}
	if supportedSchemaField(schema, "Ignored", make(map[reflect.Type]bool)) {
		t.Fatal("supportedSchemaField(ignored field) = true")
	}
	if supportedSchemaField(schema, "missing", map[reflect.Type]bool{schema: true}) {
		t.Fatal("supportedSchemaField(visited schema) = true")
	}
	if supportedSchemaField(reflect.TypeFor[string](), "missing", make(map[reflect.Type]bool)) {
		t.Fatal("supportedSchemaField(non-struct schema) = true")
	}
}
