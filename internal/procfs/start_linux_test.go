//go:build linux

package procfs

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestStartTimeReadsCurrentProcess(t *testing.T) {
	t.Parallel()

	process := int32(os.Getpid()) //nolint:gosec // Linux represents a positive native PID as int32.
	if generation, valid := StartTime(process); !valid || generation == 0 {
		t.Fatalf("StartTime(current) = %d, %t", generation, valid)
	}
	for _, invalid := range []int32{0, -1, 1<<30 + 1} {
		if generation, valid := StartTime(invalid); valid || generation != 0 {
			t.Fatalf("StartTime(%d) = %d, %t", invalid, generation, valid)
		}
	}
}

func TestDecodeStartTime(t *testing.T) {
	t.Parallel()

	valid := []byte("1 (process with ) character) S" + strings.Repeat(" 0", processStartTimeIndex-1) + " 123")
	if generation, ok := decodeStartTime(valid); !ok || generation != 123 {
		t.Fatalf("decodeStartTime(valid) = %d, %t", generation, ok)
	}

	for _, value := range [][]byte{
		nil,
		[]byte("1 (process)"),
		[]byte("1 (process) S 0"),
		[]byte("1 (process) S" + strings.Repeat(" 0", processStartTimeIndex+1)),
		[]byte("1 (process) S" + strings.Repeat(" 0", processStartTimeIndex-1) + " invalid"),
	} {
		if generation, ok := decodeStartTime(value); ok || generation != 0 {
			t.Fatalf("decodeStartTime(%q) = %d, %t", value, generation, ok)
		}
	}
}

func TestReadStartTimeRejectsIOFailures(t *testing.T) {
	t.Parallel()

	for _, reader := range []io.ReadCloser{
		testReadCloser{Reader: testFailingReader{}},
		testReadCloser{Reader: strings.NewReader("1 (x)"), closeErr: io.ErrClosedPipe},
		testReadCloser{Reader: bytes.NewReader(make([]byte, maximumProcessStatBytes+1))},
	} {
		if generation, valid := readStartTime(reader); valid || generation != 0 {
			t.Fatalf("readStartTime() = %d, %t", generation, valid)
		}
	}
}

type testReadCloser struct {
	io.Reader

	closeErr error
}

func (reader testReadCloser) Close() error { return reader.closeErr }

type testFailingReader struct{}

func (testFailingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
