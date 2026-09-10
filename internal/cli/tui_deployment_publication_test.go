package cli

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDeploymentPublicationReadIsBounded(t *testing.T) {
	t.Parallel()
	const name = "compose.yaml"
	for _, size := range []int64{maximumComposeSourceBytes, maximumComposeSourceBytes + 4096} {
		t.Run(strconv.FormatInt(size, 10), func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, name)
			file, err := os.Create(path) //nolint:gosec // Test-owned sparse file.
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if err = file.Truncate(size); err != nil {
				t.Fatal(err)
			}
			operations := defaultDeploymentEntryOperations()
			content, err := operations.readFile(file)
			if err != nil {
				t.Fatal(err)
			}
			offset, err := file.Seek(0, io.SeekCurrent)
			if err != nil || offset != min(size, maximumComposeSourceBytes+1) || int64(len(content)) != offset {
				t.Fatalf("publication read consumed %d bytes (length %d), error %v", offset, len(content), err)
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			snapshot := readDeploymentEntry(root, name, operations)
			defer snapshot.close()
			if (snapshot.info != nil) != (size <= maximumComposeSourceBytes) {
				t.Fatalf("publication snapshot accepted oversized source: %d", size)
			}
		})
	}
}
