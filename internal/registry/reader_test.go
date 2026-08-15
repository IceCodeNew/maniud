package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type controlledReader struct {
	reader   io.Reader
	readErr  error
	closeErr error
	closed   bool
}

func (reader *controlledReader) Read(value []byte) (int, error) {
	if reader.readErr != nil {
		return 0, reader.readErr
	}

	count, err := reader.reader.Read(value)
	if errors.Is(err, io.EOF) {
		return count, io.EOF
	}

	if err != nil {
		return count, fmt.Errorf("read controlled test content: %w", err)
	}

	return count, nil
}

func (reader *controlledReader) Close() error {
	reader.closed = true

	return reader.closeErr
}

func TestReadVerified(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"value":1}`)
	descriptorValue := descriptorForTest(raw, ocispec.MediaTypeImageManifest)

	result, err := readVerified(io.NopCloser(bytes.NewReader(raw)), descriptorValue, int64(len(raw)))
	if err != nil || !bytes.Equal(result, raw) {
		t.Fatalf("readVerified() = %q, %v", result, err)
	}
}

func TestReadVerifiedRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"value":1}`)
	valid := descriptorForTest(raw, ocispec.MediaTypeImageManifest)
	readFailure := io.ErrUnexpectedEOF
	closeFailure := io.ErrClosedPipe

	tests := []struct {
		name       string
		reader     io.ReadCloser
		descriptor ocispec.Descriptor
		maximum    int64
		want       error
	}{
		{name: "nil reader", descriptor: valid, maximum: int64(len(raw)), want: ErrProtocol},
		{
			name:       "invalid declared size",
			reader:     &controlledReader{reader: bytes.NewReader(raw)},
			descriptor: ocispec.Descriptor{Size: -1},
			maximum:    int64(len(raw)),
			want:       ErrProtocol,
		},
		{
			name:       "read error",
			reader:     &controlledReader{reader: bytes.NewReader(raw), readErr: readFailure},
			descriptor: valid,
			maximum:    int64(len(raw)),
			want:       readFailure,
		},
		{
			name:       "close error",
			reader:     &controlledReader{reader: bytes.NewReader(raw), closeErr: closeFailure},
			descriptor: valid,
			maximum:    int64(len(raw)),
			want:       closeFailure,
		},
		{
			name:       "digest mismatch",
			reader:     io.NopCloser(bytes.NewReader([]byte(`{"value":2}`))),
			descriptor: valid,
			maximum:    int64(len(raw)),
			want:       ErrProtocol,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := readVerified(test.reader, test.descriptor, test.maximum)
			if !errors.Is(err, test.want) {
				t.Fatalf("readVerified() error = %v, want %v", err, test.want)
			}
		})
	}
}
