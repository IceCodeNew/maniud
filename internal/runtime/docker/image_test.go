package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const testImageReferenceEvidence = "reference"

var errImageTestTransport = errors.New("image test transport failed")

func TestProbeImageProvesResolvedPlatformIdentity(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertImageProbeRequest(t, request, expected)
		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response, imageInspectDocument(expected, true))
	}))

	probe, err := client.ProbeImage(context.Background(), expected)
	if err != nil || !probe.Matches(expected) || probe.State != application.ImageProbeObserved {
		t.Fatalf("ProbeImage() = %#v, %v", probe, err)
	}
}

func TestProbeImageAcceptsClassicImageStoreProof(t *testing.T) {
	t.Parallel()

	expected := testSingleManifestImageIdentity(t)
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response, imageInspectDocument(expected, false))
	}))

	probe, err := client.ProbeImage(context.Background(), expected)
	if err != nil || !probe.Matches(expected) {
		t.Fatalf("ProbeImage(classic) = %#v, %v", probe, err)
	}
}

func TestProbeImageSeparatesValidAbsenceFromUnknown(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantMissing bool
	}{
		{
			name:        testMissingValue,
			status:      http.StatusNotFound,
			contentType: jsonContentType,
			body:        `{"message":"No such image"}`,
			wantMissing: true,
		},
		{
			name:        "malformed missing",
			status:      http.StatusNotFound,
			contentType: jsonContentType,
			body:        `{}`,
			wantMissing: false,
		},
		{
			name:        "server failure",
			status:      http.StatusInternalServerError,
			contentType: jsonContentType,
			body:        `{"message":"failed"}`,
			wantMissing: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set(contentTypeHeader, test.contentType)
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))

			probe, err := client.ProbeImage(context.Background(), expected)
			if test.wantMissing {
				if err != nil || probe.State != application.ImageProbeMissing || probe.Matches(expected) {
					t.Fatalf("ProbeImage() = %#v, %v", probe, err)
				}

				return
			}

			if err == nil || probe != emptyImageProbe() {
				t.Fatalf("ProbeImage(unknown) = %#v, %v", probe, err)
			}
		})
	}
}

func TestProbeImageRejectsConflictingEvidence(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)

	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "config", mutate: func(value string) string {
			return strings.Replace(value, expected.ImageConfig.String(), domain.Hash([]byte("other config")).String(), 1)
		}},
		{name: testImageReferenceEvidence, mutate: func(value string) string {
			return strings.Replace(value, expected.ReferenceDigest.String(), domain.Hash([]byte("other reference")).String(), 1)
		}},
		{name: "manifest", mutate: func(value string) string {
			return strings.Replace(value, expected.PlatformManifest.String(), domain.Hash([]byte("other manifest")).String(), 1)
		}},
		{name: "platform", mutate: func(value string) string {
			return strings.Replace(value, `"Architecture":"amd64"`, `"Architecture":"arm64"`, 1)
		}},
		{name: "missing config", mutate: func(value string) string {
			return strings.Replace(value, `"Config":{}`, `"Config":null`, 1)
		}},
		{name: "negative size", mutate: func(value string) string {
			return strings.Replace(value, `"Size":0`, `"Size":-1`, 1)
		}},
		{name: "invalid repository digest", mutate: func(value string) string {
			return strings.Replace(value, `"RepoDigests":[`, `"RepoDigests":["`+testInvalidLiteral+`",`, 1)
		}},
		{name: "invalid descriptor", mutate: func(value string) string {
			return strings.Replace(value, `"size":123`, `"size":0`, 1)
		}},
		{name: "missing multi-platform descriptor", mutate: func(value string) string {
			return strings.Replace(value, `,"Descriptor":`+
				fmt.Sprintf(`{"mediaType":%q,"digest":%q,"size":123}`, ociManifestMediaType,
					expected.PlatformManifest.String()), "", 1)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set(contentTypeHeader, jsonContentType)
				_, _ = io.WriteString(response, test.mutate(imageInspectDocument(expected, true)))
			}))

			probe, err := client.ProbeImage(context.Background(), expected)
			if err == nil || probe != emptyImageProbe() {
				t.Fatalf("ProbeImage(conflict) = %#v, %v", probe, err)
			}
		})
	}
}

func TestProbeImageRejectsInvalidRequestAndResponse(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	invalid := expected
	invalid.Reference = testInvalidLiteral

	probe, err := connectedTestClient(t, nil).ProbeImage(context.Background(), invalid)
	if err == nil || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(invalid) = %#v, %v", probe, err)
	}

	probe, err = (*Client)(nil).ProbeImage(context.Background(), expected)
	if err == nil || probe != emptyImageProbe() {
		t.Fatalf("nil ProbeImage() = %#v, %v", probe, err)
	}

	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, plainTextContentType)
		_, _ = io.WriteString(response, imageInspectDocument(expected, true))
	}))

	probe, err = client.ProbeImage(context.Background(), expected)
	if err == nil || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(content type) = %#v, %v", probe, err)
	}
}

func TestProbeImageContainsTransportAndProtocolFailures(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)

	transportFailure := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errImageTestTransport
	}))
	transportFailure.version = testDockerVersion()

	probe, err := transportFailure.ProbeImage(context.Background(), expected)
	if !errors.Is(err, ErrUnavailable) || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(transport) = %#v, %v", probe, err)
	}

	invalidVersion := connectedTestClient(t, nil)
	invalidVersion.version.Protocol = ""

	probe, err = invalidVersion.ProbeImage(context.Background(), expected)
	if !errors.Is(err, ErrProtocol) || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(version) = %#v, %v", probe, err)
	}

	malformed := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response, `{`)
	}))

	probe, err = malformed.ProbeImage(context.Background(), expected)
	if !errors.Is(err, ErrProtocol) || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(malformed) = %#v, %v", probe, err)
	}
}

func TestImagePlatformIncludesVariant(t *testing.T) {
	t.Parallel()

	got := imagePlatform(domain.Platform{
		OS:           dockerOperatingSystem,
		Architecture: dockerArchitectureARM64,
		Variant:      dockerARM64Variant,
	})

	want := "{\"architecture\":\"arm64\",\"os\":\"linux\",\"variant\":\"v8\"}"
	if got != want {
		t.Fatalf("imagePlatform() = %q", got)
	}
}

func testImageIdentity(t *testing.T) domain.ImageIdentity {
	t.Helper()

	referenceDigest := domain.Hash([]byte(testImageReferenceEvidence))
	reference := "example.com/team/api:1@" + referenceDigest.String()

	return domain.ImageIdentity{
		Reference:       reference,
		ReferenceDigest: referenceDigest,
		Platform: domain.Platform{
			OS:           dockerOperatingSystem,
			Architecture: dockerArchitectureAMD64,
			Variant:      "",
		},
		PlatformManifest: domain.Hash([]byte("platform manifest")),
		ImageConfig:      domain.Hash([]byte("image config")),
		Entrypoint:       []string{"/usr/local/bin/api"},
		Command:          []string{"serve"},
	}
}

func testSingleManifestImageIdentity(t *testing.T) domain.ImageIdentity {
	t.Helper()

	expected := testImageIdentity(t)
	expected.PlatformManifest = expected.ReferenceDigest
	expected.Reference = "example.com/team/api:1@" + expected.ReferenceDigest.String()

	return expected
}

func testDockerVersion() Version {
	return Version{
		Protocol:     maximumAPIVersion,
		Minimum:      minimumAPIVersion,
		Maximum:      maximumAPIVersion,
		Product:      testProduct,
		OS:           dockerOperatingSystem,
		Architecture: dockerArchitectureAMD64,
	}
}

func emptyImageProbe() application.ImageProbe {
	return application.ImageProbe{State: application.ImageProbeUnknown, Image: emptyImage()}
}

func assertImageProbeRequest(t *testing.T, request *http.Request, expected domain.ImageIdentity) {
	t.Helper()

	wantPath := "/v1.54/images/example.com/team/api@" + expected.ReferenceDigest.String() + "/json"
	if request.Method != http.MethodGet || request.URL.Path != wantPath ||
		request.URL.Query().Get("platform") != `{"architecture":"amd64","os":"linux"}` {
		t.Fatalf("image probe request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
	}
}

func imageInspectDocument(expected domain.ImageIdentity, descriptor bool) string {
	target := ""
	if descriptor {
		target = fmt.Sprintf(
			`,"Descriptor":{"mediaType":%q,"digest":%q,"size":123}`,
			ociManifestMediaType,
			expected.PlatformManifest.String(),
		)
	}

	return fmt.Sprintf(
		`{"Id":%q,"RepoDigests":[%q],"Config":{},"Architecture":%q,"Variant":%q,`+
			`"Os":%q,"Size":0%s}`,
		expected.ImageConfig.String(),
		"example.com/team/api@"+expected.ReferenceDigest.String(),
		expected.Platform.Architecture,
		expected.Platform.Variant,
		expected.Platform.OS,
		target,
	)
}
