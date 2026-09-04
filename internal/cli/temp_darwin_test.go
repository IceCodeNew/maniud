//go:build darwin

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	temporaryRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "resolve test temporary directory: %v\n", err)
		os.Exit(1)
	}
	if err = os.Setenv("TMPDIR", temporaryRoot); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "set test temporary directory: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
