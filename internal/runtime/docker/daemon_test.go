package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInspectDaemon(t *testing.T) {
	t.Parallel()

	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1.54/info" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}

		response.Header().Set("Content-Type", jsonContentType)
		_, _ = io.WriteString(response, daemonDocument(
			"engine-id", "overlay2", testOS, dockerMachineAMD64, testProduct, true,
		))
	}))

	got, err := client.InspectDaemon(context.Background())
	if err != nil {
		t.Fatalf("InspectDaemon() error = %v", err)
	}

	want := Daemon{
		ID:           "engine-id",
		Driver:       "overlay2",
		OS:           testOS,
		Architecture: testArchitecture,
		Rootless:     true,
	}
	if got != want {
		t.Fatalf("InspectDaemon() = %#v, want %#v", got, want)
	}
}

//nolint:funlen // The table is the strict daemon-info rejection corpus.
func TestInspectDaemonRejectsUnknownEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "failure status", status: http.StatusInternalServerError, contentType: jsonContentType, body: `{}`},
		{name: "wrong content type", status: http.StatusOK, contentType: "text/plain", body: `{}`},
		{name: "malformed", status: http.StatusOK, contentType: jsonContentType, body: `{"ID":`},
		{
			name:        "duplicate field",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        `{"ID":"one","ID":"two"}`,
		},
		{
			name:        "unknown field",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        `{"Unknown":true}`,
		},
		{
			name:        "oversized",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        `{"ID":"` + strings.Repeat("x", maximumJSONBytes) + `"}`,
		},
		{
			name:        "missing identity",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        daemonDocument("", "overlay2", testOS, testArchitecture, testProduct, false),
		},
		{
			name:        "invalid identity",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        daemonDocument("engine id", "overlay2", testOS, testArchitecture, testProduct, false),
		},
		{
			name:        "invalid driver",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        daemonDocument("engine-id", "bad\ndriver", testOS, testArchitecture, testProduct, false),
		},
		{
			name:        "OS drift",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        daemonDocument("engine-id", "overlay2", "windows", testArchitecture, testProduct, false),
		},
		{
			name:        "architecture drift",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        daemonDocument("engine-id", "overlay2", testOS, "arm64", testProduct, false),
		},
		{
			name:        "product drift",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        daemonDocument("engine-id", "overlay2", testOS, testArchitecture, "29.7.3", false),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", test.contentType)
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))

			got, err := client.InspectDaemon(context.Background())

			var emptyDaemon Daemon
			if got != emptyDaemon || !errors.Is(err, ErrProtocol) {
				t.Fatalf("InspectDaemon() = %#v, %v; want zero, ErrProtocol", got, err)
			}
		})
	}
}

func TestInspectDaemonContainsUnknownTransportOutcome(t *testing.T) {
	t.Parallel()

	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	client.version = testVersion()

	got, err := client.InspectDaemon(context.Background())

	var emptyDaemon Daemon
	if got != emptyDaemon || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("InspectDaemon() = %#v, %v; want zero, ErrUnavailable", got, err)
	}

	client.version.Protocol = ""

	got, err = client.InspectDaemon(context.Background())
	if got != emptyDaemon || !errors.Is(err, ErrProtocol) {
		t.Fatalf("InspectDaemon(invalid client) = %#v, %v; want zero, ErrProtocol", got, err)
	}
}

func TestDaemonArchitectureMatchesMachineAndBinaryNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		machine string
		binary  string
		want    string
		valid   bool
	}{
		{machine: dockerMachineAMD64, binary: dockerArchitectureAMD64, want: dockerArchitectureAMD64, valid: true},
		{machine: dockerArchitectureAMD64, binary: dockerArchitectureAMD64, want: dockerArchitectureAMD64, valid: true},
		{machine: dockerMachineARM64, binary: dockerArchitectureARM64, want: dockerArchitectureARM64, valid: true},
		{machine: dockerArchitectureARM64, binary: dockerArchitectureARM64, want: dockerArchitectureARM64, valid: true},
		{machine: dockerMachineARM64, binary: dockerArchitectureAMD64, want: dockerArchitectureARM64, valid: false},
		{
			machine: testUnsupportedArchitecture,
			binary:  testUnsupportedArchitecture,
			want:    testUnsupportedArchitecture,
			valid:   true,
		},
		{machine: "", binary: "", want: "", valid: false},
	}

	for _, test := range tests {
		got, valid := daemonArchitecture(test.machine, test.binary)
		if got != test.want || valid != test.valid {
			t.Errorf("daemonArchitecture(%q, %q) = %q, %t; want %q, %t",
				test.machine, test.binary, got, valid, test.want, test.valid)
		}
	}
}

func connectedTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := testClient(server.Client().Transport)
	client.baseURL = *mustParseURL(t, server.URL)
	client.version = testVersion()

	return client
}

func testVersion() Version {
	return Version{
		Protocol:     maximumAPIVersion,
		Minimum:      "1.40",
		Maximum:      maximumAPIVersion,
		Product:      testProduct,
		OS:           testOS,
		Architecture: testArchitecture,
	}
}

func daemonDocument(identifier, driver, osName, architecture, product string, rootless bool) string {
	security := `[]`
	if rootless {
		security = `["name=seccomp,profile=builtin","name=rootless"]`
	}

	return `{"ID":` + quoteJSON(identifier) + `,"Driver":` + quoteJSON(driver) + `,"OSType":` + quoteJSON(osName) +
		`,"Architecture":` + quoteJSON(architecture) + `,"ServerVersion":` + quoteJSON(product) +
		`,"SecurityOptions":` + security + `}`
}
