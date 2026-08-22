//go:build darwin

package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalBackupParentBoundaries(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing")
	if got := canonicalBackupParent(missing); got != missing {
		t.Fatalf("canonicalBackupParent(missing) = %q", got)
	}

	logical, err := os.MkdirTemp("/tmp", "maniud-canonical-") //nolint:usetesting // The /tmp alias is the boundary under test.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(logical) })
	physical, err := filepath.EvalSymlinks(logical)
	if err != nil || physical == logical || canonicalBackupParent(logical) != physical {
		t.Fatalf("canonicalBackupParent(alias) = %q, physical %q, error %v", canonicalBackupParent(logical), physical, err)
	}
	if got := canonicalBackupParent(physical); got != physical {
		t.Fatalf("canonicalBackupParent(physical) = %q", got)
	}

	target := filepath.Join(physical, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(physical, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if got := canonicalBackupParent(symlink); got != symlink {
		t.Fatalf("canonicalBackupParent(non-alias symlink) = %q", got)
	}
}
