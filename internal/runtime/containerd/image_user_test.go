package containerd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"testing"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

func TestResolveImageUserReadsVerifiedContainerdLayers(t *testing.T) {
	t.Parallel()

	layer := containerdUserLayer(t)
	identity, image, content := testContainerdImageGraph(t, layer)
	client := testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
	client.images = fakeImagesClient{response: &imagesapi.GetImageResponse{Image: image}}
	client.content = content

	resolved, err := client.ResolveImageUser(t.Context(), identity, "service")
	if err != nil || resolved != "1001:1002" {
		t.Fatalf("ResolveImageUser() = %q, %v", resolved, err)
	}
	if !sameContainerdImage(identity, identity) {
		t.Fatal("sameContainerdImage(identity) = false")
	}
	drifted := identity
	drifted.ImageConfig = domain.Hash([]byte("drifted"))
	if sameContainerdImage(identity, drifted) {
		t.Fatal("sameContainerdImage(drifted) = true")
	}
	if _, err = (*Client)(nil).ResolveImageUser(
		t.Context(), identity, "service",
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ResolveImageUser(nil client) = %v", err)
	}
}

func TestResolveImageUserContainsIdentityGraphAndFilesystemFailures(t *testing.T) {
	t.Parallel()

	layer := containerdUserLayer(t)
	identity, image, content := testContainerdImageGraph(t, layer)

	drifted := identity
	drifted.ImageConfig = domain.Hash([]byte("drifted"))
	client := testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
	client.images = fakeImagesClient{response: &imagesapi.GetImageResponse{Image: image}}
	client.content = content
	if _, err := client.ResolveImageUser(t.Context(), drifted, "service"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ResolveImageUser(identity drift) = %v", err)
	}

	manifestDigest := identity.PlatformManifest.String()
	manifestRaw := content[manifestDigest]
	fault := &faultContentClient{
		mappedContentClient: content,
		digest:              manifestDigest,
		infoResponse: &contentapi.InfoResponse{Info: &contentapi.Info{
			Digest: manifestDigest, Size: 0,
		}},
	}
	client = testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
	client.images = fakeImagesClient{response: &imagesapi.GetImageResponse{Image: image}}
	client.content = fault
	if _, err := client.ResolveImageUser(t.Context(), identity, "service"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ResolveImageUser(layer metadata failure) = %v, manifest size %d", err, len(manifestRaw))
	}

	invalidIdentity, invalidImage, invalidContent := testContainerdImageGraph(t, []byte("not a compressed layer"))
	client = testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
	client.images = fakeImagesClient{response: &imagesapi.GetImageResponse{Image: invalidImage}}
	client.content = invalidContent
	if _, err := client.ResolveImageUser(t.Context(), invalidIdentity, "service"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ResolveImageUser(filesystem failure) = %v", err)
	}
}

func TestDecodeImageUserManifestRejectsExtensionsAndDrift(t *testing.T) {
	t.Parallel()

	layer := containerdUserLayer(t)
	identity, _, content := testContainerdImageGraph(t, layer)
	raw := content[identity.PlatformManifest.String()]
	manifest, valid := decodeImageUserManifest(raw, identity)
	if !valid || len(manifest.Layers) != 1 {
		t.Fatalf("decodeImageUserManifest(valid) = %#v, %t", manifest, valid)
	}

	for _, mutate := range []func(*ocispec.Manifest){
		func(value *ocispec.Manifest) { value.SchemaVersion = 1 },
		func(value *ocispec.Manifest) { value.ArtifactType = "application/example" },
		func(value *ocispec.Manifest) { value.Subject = &ocispec.Descriptor{} },
		func(value *ocispec.Manifest) { value.Config.Digest = digest.FromString("other") },
		func(value *ocispec.Manifest) { value.Config.Size = 0 },
		func(value *ocispec.Manifest) { value.Layers[0].Digest = "invalid" },
		func(value *ocispec.Manifest) { value.Layers[0].Size = 0 },
		func(value *ocispec.Manifest) { value.Layers[0].MediaType = "" },
	} {
		var value ocispec.Manifest
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		mutate(&value)
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, valid := decodeImageUserManifest(encoded, identity); valid {
			t.Fatalf("decodeImageUserManifest(%s) accepted", encoded)
		}
	}
	if _, valid := decodeImageUserManifest([]byte("invalid"), identity); valid {
		t.Fatal("decodeImageUserManifest(malformed) accepted")
	}
}

func TestImageUserContentDescriptorBoundaries(t *testing.T) {
	t.Parallel()

	digestValue := digest.FromString("content")
	target := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digestValue,
		Size:      7,
	}
	if reader, err := (*Client)(nil).openVerifiedContent(t.Context(), target); reader != nil ||
		!errors.Is(err, ErrUnavailable) {
		t.Fatalf("openVerifiedContent(nil) = %#v, %v", reader, err)
	}
	client := testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
	client.content = fakeContentClient{infoErr: status.Error(codes.NotFound, "missing")}
	if _, err := client.contentDescriptor(t.Context(), domain.Hash([]byte("manifest"))); err == nil {
		t.Fatal("contentDescriptor(info failure) succeeded")
	}
	manifestIdentity := domain.Hash([]byte("manifest"))
	for _, info := range []*contentapi.InfoResponse{
		{},
		{Info: &contentapi.Info{Digest: domain.Hash([]byte("other")).String(), Size: 1}},
		{Info: &contentapi.Info{Digest: manifestIdentity.String(), Size: 0}},
		{Info: &contentapi.Info{Digest: manifestIdentity.String(), Size: maximumMetadataBytes + 1}},
	} {
		client.content = fakeContentClient{info: info}
		if _, err := client.contentDescriptor(t.Context(), manifestIdentity); !errors.Is(err, ErrProtocol) {
			t.Fatalf("contentDescriptor(%#v) = %v", info, err)
		}
	}
	if reader, err := client.openVerifiedContent(t.Context(), ocispec.Descriptor{}); reader != nil ||
		!errors.Is(err, ErrProtocol) {
		t.Fatalf("openVerifiedContent(invalid descriptor) = %#v, %v", reader, err)
	}
}

func TestWriteVerifiedContentChunkBoundaries(t *testing.T) {
	t.Parallel()

	digestValue := digest.FromString("content")
	if _, err := writeVerifiedContentChunk(
		nil, 0, 7, digestValue.Algorithm().Hash(), io.Discard,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("writeVerifiedContentChunk(nil) = %v", err)
	}
	response := &contentapi.ReadContentResponse{Offset: 1, Data: []byte("x")}
	if _, err := writeVerifiedContentChunk(
		response, 0, 7, digestValue.Algorithm().Hash(), io.Discard,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("writeVerifiedContentChunk(offset) = %v", err)
	}
	response = &contentapi.ReadContentResponse{Offset: 0, Data: []byte("content")}
	if _, err := writeVerifiedContentChunk(
		response, 0, 7, digestValue.Algorithm().Hash(), shortWriter{},
	); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeVerifiedContentChunk(short write) = %v", err)
	}
	if _, err := writeVerifiedContentChunk(
		response, 0, 7, digestValue.Algorithm().Hash(), errorWriter{err: errContainerdTest},
	); !errors.Is(err, errContainerdTest) {
		t.Fatalf("writeVerifiedContentChunk(write failure) = %v", err)
	}
}

type imageUserLayersFailureTest struct {
	name    string
	raw     []byte
	content func(string, []byte) contentClient
}

func TestImageUserLayersRejectInvalidContentGraph(t *testing.T) {
	t.Parallel()

	config := domain.Hash([]byte("config"))
	manifest := ocispec.Manifest{Versioned: ocispec.Manifest{}.Versioned}
	manifest.SchemaVersion = 2
	manifest.Config = ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.Digest(config.String()), Size: 1,
	}
	validLayerDigest := digest.FromString("layer")
	manifest.Layers = []ocispec.Descriptor{{
		MediaType: ocispec.MediaTypeImageLayer, Digest: validLayerDigest, Size: 1,
	}}

	for _, test := range imageUserLayersFailureTests(t, manifest) {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source, normalizeErr := imageref.Normalize("example.com/team/api:1")
			if normalizeErr != nil {
				t.Fatal(normalizeErr)
			}
			manifestDigest := digest.FromBytes(test.raw).String()
			manifestIdentity, parseErr := domain.ParseDigest(manifestDigest)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			expected := domain.ImageIdentity{
				Origin: domain.ImageOriginRegistry, Reference: source.String(), Platform: domain.Platform{
					OS: containerdPlatformOS, Architecture: containerdArchitectureAMD64,
				},
				PlatformManifest: manifestIdentity, ImageConfig: config,
			}
			client := testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
			client.content = test.content(manifestDigest, test.raw)
			if _, err := client.imageUserLayers(t.Context(), source, expected); err == nil {
				t.Fatal("imageUserLayers() succeeded")
			}
		})
	}
}

func TestImageUserLayersContainsDescriptorFailure(t *testing.T) {
	t.Parallel()

	source, err := imageref.Normalize("example.com/team/api:1")
	if err != nil {
		t.Fatal(err)
	}
	client := testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
	client.content = fakeContentClient{infoErr: errContainerdTest}
	expected := domain.ImageIdentity{PlatformManifest: domain.Hash([]byte("manifest"))}
	if _, err := client.imageUserLayers(t.Context(), source, expected); err == nil {
		t.Fatal("imageUserLayers(content descriptor failure) succeeded")
	}
}

func imageUserLayersFailureTests(
	t *testing.T,
	manifest ocispec.Manifest,
) []imageUserLayersFailureTest {
	t.Helper()
	withLayerURL := manifest
	withLayerURL.Layers[0].URLs = []string{"https://example.invalid/layer"}

	return []imageUserLayersFailureTest{
		{
			name: "manifest read",
			raw:  mustJSON(t, manifest),
			content: func(manifestDigest string, raw []byte) contentClient {
				return &faultContentClient{
					mappedContentClient: mappedContentClient{manifestDigest: raw},
					digest:              manifestDigest,
					infoResponse:        nil,
					infoErr:             errContainerdTest,
				}
			},
		},
		{
			name: "manifest syntax",
			raw:  []byte("invalid"),
			content: func(manifestDigest string, raw []byte) contentClient {
				return mappedContentClient{manifestDigest: raw}
			},
		},
		{
			name: "layer URL",
			raw:  mustJSON(t, withLayerURL),
			content: func(manifestDigest string, raw []byte) contentClient {
				return mappedContentClient{manifestDigest: raw}
			},
		},
	}
}

func TestCopyVerifiedContentFailureMatrix(t *testing.T) {
	t.Parallel()

	data := []byte("content")
	target := contentTarget(data)
	tests := []struct {
		name        string
		content     contentClient
		destination io.Writer
	}{
		{
			name:    "info error",
			content: fakeContentClient{infoErr: status.Error(codes.Unavailable, "down")},
		},
		{
			name:    "info mismatch",
			content: fakeContentClient{info: verifiedContentInfo(target, target.Size+1)},
		},
		{
			name: "read error",
			content: fakeContentClient{
				info:      verifiedContentInfo(target, target.Size),
				streamErr: status.Error(codes.Unavailable, "down"),
			},
		},
		{
			name: "receive error",
			content: fakeContentClient{
				info:   verifiedContentInfo(target, target.Size),
				stream: &fakeContentStream{err: status.Error(codes.Unavailable, "down")},
			},
		},
		{
			name: "chunk error",
			content: fakeContentClient{
				info:   verifiedContentInfo(target, target.Size),
				stream: &fakeContentStream{responses: []*contentapi.ReadContentResponse{{Data: data}}},
			},
			destination: errorWriter{err: errContainerdTest},
		},
		{
			name: "digest mismatch",
			content: fakeContentClient{
				info:   verifiedContentInfo(target, target.Size),
				stream: &fakeContentStream{responses: []*contentapi.ReadContentResponse{{Data: []byte("contend")}}},
			},
			destination: io.Discard,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &Client{content: test.content}
			if err := client.copyVerifiedContent(t.Context(), target, test.destination); err == nil {
				t.Fatal("copyVerifiedContent() succeeded")
			}
		})
	}
}

func verifiedContentInfo(target ocispec.Descriptor, size int64) *contentapi.InfoResponse {
	return &contentapi.InfoResponse{Info: &contentapi.Info{
		Digest: target.Digest.String(),
		Size:   size,
	}}
}

func TestCopyVerifiedContentRejectsExcessiveChunks(t *testing.T) {
	t.Parallel()

	manyData := bytes.Repeat([]byte{'x'}, maximumContentChunks+1)
	manyTarget := contentTarget(manyData)
	responses := make([]*contentapi.ReadContentResponse, maximumContentChunks)
	for index := range responses {
		responses[index] = &contentapi.ReadContentResponse{Offset: int64(index), Data: []byte{'x'}}
	}
	client := &Client{content: fakeContentClient{
		info: &contentapi.InfoResponse{Info: &contentapi.Info{
			Digest: manyTarget.Digest.String(), Size: manyTarget.Size,
		}},
		stream: &fakeContentStream{responses: responses},
	}}
	if err := client.copyVerifiedContent(t.Context(), manyTarget, io.Discard); !errors.Is(err, ErrProtocol) {
		t.Fatalf("copyVerifiedContent(chunk limit) = %v", err)
	}
}

func contentTarget(data []byte) ocispec.Descriptor {
	return ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer, Digest: digest.FromBytes(data), Size: int64(len(data)),
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func containerdUserLayer(t *testing.T) []byte {
	t.Helper()

	var uncompressed bytes.Buffer
	tarWriter := tar.NewWriter(&uncompressed)
	passwd := []byte("root:x:0:0:root:/root:/bin/sh\nservice:x:1001:1002::/srv/service:/bin/sh\n")
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "etc/passwd", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(passwd)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(passwd); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}

	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	if _, err := io.Copy(gzipWriter, &uncompressed); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	return compressed.Bytes()
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) { return len(value) - 1, nil }

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }
