package imagerootfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const (
	testPasswd = "root:x:0:0:root:/root:/bin/sh\n" +
		"alice:x:1001:1002:Alice:/home/alice:/bin/sh\n"
	testGroup = "root:x:0:\nstaff:x:1002:alice\nwheel:x:1003:\n"
)

var errImageRootFSStreamTest = errors.New("stream failed")

func TestResolveUserExpressions(t *testing.T) {
	t.Parallel()
	image := imageWithLayers(t, layerTar(t,
		tarEntry{name: passwdPath, body: testPasswd},
		tarEntry{name: groupPath, body: testGroup},
	))

	for _, test := range []struct {
		name, specification, want string
	}{
		{name: "named user", specification: "alice", want: "1001:1002"},
		{name: "named user and group", specification: "alice:wheel", want: "1001:1003"},
		{name: "numeric uid defaults gid", specification: "4242", want: "4242:0"},
		{name: "numeric uid and named group", specification: "4242:staff", want: "4242:1002"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveUser(context.Background(), image, test.specification)
			if err != nil || got != test.want {
				t.Fatalf("ResolveUser(%q) = %q, %v; want %q", test.specification, got, err, test.want)
			}
		})
	}
}

func TestResolveNumericUserWithoutAccountFiles(t *testing.T) {
	t.Parallel()

	image := imageWithLayers(t, layerTar(t))
	for _, test := range []struct {
		specification string
		want          string
	}{
		{specification: "4242", want: "4242:0"},
		{specification: "4242:4343", want: "4242:4343"},
	} {
		got, err := ResolveUser(context.Background(), image, test.specification)
		if err != nil || got != test.want {
			t.Fatalf("ResolveUser(%q) = %q, %v; want %q", test.specification, got, err, test.want)
		}
	}
}

func TestResolveUserUsesFinalLayerView(t *testing.T) {
	t.Parallel()
	lower := layerTar(t, tarEntry{name: passwdPath, body: testPasswd}, tarEntry{name: groupPath, body: testGroup})

	t.Run("upper file replaces lower file", func(t *testing.T) {
		t.Parallel()
		upper := layerTar(t, tarEntry{name: passwdPath, body: "alice:x:2001:2002::/:/bin/sh\n"})
		got, err := ResolveUser(context.Background(), imageWithLayers(t, lower, upper), "alice")
		if err != nil || got != "2001:2002" {
			t.Fatalf("ResolveUser() = %q, %v; want 2001:2002", got, err)
		}
	})

	t.Run("whiteout removes lower passwd", func(t *testing.T) {
		t.Parallel()
		whiteout := layerTar(t, tarEntry{name: "etc/.wh.passwd"})
		_, err := ResolveUser(context.Background(), imageWithLayers(t, lower, whiteout), "alice")
		if !errors.Is(err, ErrUnknownUser) {
			t.Fatalf("ResolveUser() error = %v; want ErrUnknownUser", err)
		}
	})
}

func TestResolveUserMissingOrUnknownAccounts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, specification string
		entries             []tarEntry
	}{
		{name: "no passwd", specification: "alice", entries: []tarEntry{{name: groupPath, body: testGroup}}},
		{name: "unknown user", specification: "nobody", entries: []tarEntry{{name: passwdPath, body: testPasswd}}},
		{name: "empty specification", entries: []tarEntry{{name: passwdPath, body: testPasswd}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveUser(context.Background(), imageWithLayers(t, layerTar(t, test.entries...)), test.specification)
			if !errors.Is(err, ErrUnknownUser) {
				t.Fatalf("ResolveUser() error = %v; want ErrUnknownUser", err)
			}
		})
	}
}

func TestResolveUserHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveUser(ctx, imageWithLayers(t, layerTar(t, tarEntry{name: passwdPath, body: testPasswd})), "alice")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveUser() error = %v; want context.Canceled", err)
	}
}

func TestResolveCompressedUserRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()
	valid := compressedDescriptor(t, layerTarBytes(t, tarEntry{name: passwdPath, body: testPasswd}))
	for _, test := range []struct {
		name   string
		mutate func(*CompressedLayer)
	}{
		{name: "digest", mutate: func(layer *CompressedLayer) { layer.Digest = "not-a-digest" }},
		{name: "zero size", mutate: func(layer *CompressedLayer) { layer.Size = 0 }},
		{name: "missing opener", mutate: func(layer *CompressedLayer) { layer.Open = nil }},
		{name: "media type", mutate: func(layer *CompressedLayer) { layer.MediaType = string(types.OCIConfigJSON) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := valid
			test.mutate(&layer)
			_, err := ResolveCompressedUser(context.Background(), []CompressedLayer{layer}, "alice")
			if !errors.Is(err, ErrInvalidImage) {
				t.Fatalf("ResolveCompressedUser() error = %v; want ErrInvalidImage", err)
			}
		})
	}
}

func TestResolveCompressedUserStreamFailures(t *testing.T) {
	t.Parallel()
	raw := layerTarBytes(t, tarEntry{name: passwdPath, body: testPasswd})
	valid := compressedDescriptor(t, raw)
	tests := []struct {
		name  string
		layer CompressedLayer
	}{
		{name: "open", layer: withOpen(valid, func() (io.ReadCloser, error) {
			return nil, errImageRootFSStreamTest
		})},
		{name: "read", layer: withOpen(valid, func() (io.ReadCloser, error) {
			return &errorReadCloser{err: errImageRootFSStreamTest}, nil
		})},
		{name: "integrity", layer: withOpen(valid, func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("different compressed bytes")), nil
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveCompressedUser(context.Background(), []CompressedLayer{test.layer}, "alice")
			if err == nil || got != "" {
				t.Fatalf("ResolveCompressedUser() = %q, %v; want empty result and an error", got, err)
			}
		})
	}
}

func TestCompressedLayerMetadata(t *testing.T) {
	t.Parallel()

	digest, err := v1.NewHash("sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	layer := &compressedLayer{digest: digest, size: 42, mediaType: types.OCILayer}
	gotDigest, digestErr := layer.Digest()
	gotSize, sizeErr := layer.Size()
	gotMediaType, mediaTypeErr := layer.MediaType()
	if digestErr != nil || sizeErr != nil || mediaTypeErr != nil ||
		gotDigest != digest || gotSize != 42 || gotMediaType != types.OCILayer {
		t.Fatalf(
			"compressed layer metadata = (%s, %d, %s), errors = (%v, %v, %v)",
			gotDigest, gotSize, gotMediaType, digestErr, sizeErr, mediaTypeErr,
		)
	}
}

func TestResolveArchiveUser(t *testing.T) {
	t.Parallel()
	t.Run("valid docker archive", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "image.tar")
		image := imageWithLayers(t, layerTar(t, tarEntry{name: passwdPath, body: testPasswd}))
		if err := tarball.WriteToFile(path, name.MustParseReference("example.test/accounts:latest"), image); err != nil {
			t.Fatal(err)
		}
		got, err := ResolveArchiveUser(context.Background(), path, "alice")
		if err != nil || got != "1001:1002" {
			t.Fatalf("ResolveArchiveUser() = %q, %v; want 1001:1002", got, err)
		}
	})

	t.Run("bad archive path", func(t *testing.T) {
		t.Parallel()
		_, err := ResolveArchiveUser(context.Background(), filepath.Join(t.TempDir(), "missing.tar"), "alice")
		if !errors.Is(err, ErrInvalidImage) {
			t.Fatalf("ResolveArchiveUser() error = %v; want ErrInvalidImage", err)
		}
	})
}

func TestResolveUserRejectsAccountFileBoundaryErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		entry tarEntry
	}{
		{name: "oversized", entry: tarEntry{name: passwdPath, body: strings.Repeat("x", int(maximumAccountFileSize)+1)}},
		{name: "not regular", entry: tarEntry{name: passwdPath, typeflag: tar.TypeDir}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveUser(context.Background(), imageWithLayers(t, layerTar(t, test.entry)), "alice")
			if !errors.Is(err, ErrInvalidImage) {
				t.Fatalf("ResolveUser() error = %v; want ErrInvalidImage", err)
			}
		})
	}
}

func TestReadAccountFilesBoundaries(t *testing.T) {
	t.Parallel()

	oneEntry := layerTarBytes(t, tarEntry{name: "unrelated", body: "x"})
	if _, err := readAccountFiles(context.Background(), bytes.NewReader(oneEntry), 1); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("readAccountFiles(entry limit) error = %v; want ErrInvalidImage", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readAccountFiles(cancelled, bytes.NewReader(oneEntry), 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("readAccountFiles(cancelled) error = %v; want context.Canceled", err)
	}

	if _, err := readAccountFiles(
		context.Background(), errorReader{err: io.ErrUnexpectedEOF}, 2,
	); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("readAccountFiles(header error) = %v; want ErrInvalidImage", err)
	}

	truncated := truncatedTarEntry(t, passwdPath, 5, "abc")
	if _, err := readAccountFiles(context.Background(), bytes.NewReader(truncated), 2); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("readAccountFiles(content error) = %v; want ErrInvalidImage", err)
	}
}

type tarEntry struct {
	name     string
	body     string
	typeflag byte
}

//nolint:ireturn // Tests use the upstream layer interface accepted by the production API.
func layerTar(t *testing.T, entries ...tarEntry) v1.Layer {
	t.Helper()

	return static.NewLayer(layerTarBytes(t, entries...), types.OCIUncompressedLayer)
}

func layerTarBytes(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Typeflag: typeflag, Mode: 0o644}
		if typeflag == tar.TypeReg {
			header.Size = int64(len(entry.body))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := io.WriteString(writer, entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}

func truncatedTarEntry(t *testing.T, name string, declaredSize int64, body string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	header := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: declaredSize}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatal(err)
	}

	return append(slices.Clone(buffer.Bytes()[:512]), body...)
}

//nolint:ireturn // Tests use the upstream image interface accepted by the production API.
func imageWithLayers(t *testing.T, layers ...v1.Layer) v1.Image {
	t.Helper()
	base, err := random.Image(32, 1)
	if err != nil {
		t.Fatal(err)
	}
	image, err := mutate.AppendLayers(base, layers...)
	if err != nil {
		t.Fatal(err)
	}

	return image
}

func compressedDescriptor(t *testing.T, raw []byte) CompressedLayer {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := compressed.Bytes()
	digest, _, err := v1.SHA256(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	return CompressedLayer{
		Digest: digest.String(), Size: int64(len(data)), MediaType: string(types.OCILayer),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}
}

func withOpen(layer CompressedLayer, open func() (io.ReadCloser, error)) CompressedLayer {
	layer.Open = open

	return layer
}

type errorReadCloser struct{ err error }

func (reader *errorReadCloser) Read([]byte) (int, error) { return 0, reader.err }
func (*errorReadCloser) Close() error                    { return nil }

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
