package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestReplacementBindInputBoundaries(t *testing.T) {
	t.Parallel()

	if !errors.Is(prepareUpgradeReplacementBinds(nil), ErrInvalidRequest) ||
		!errors.Is(prepareUpgradeReplacementBinds(&upgradeExecution{}), ErrInvalidRequest) {
		t.Fatal("prepareUpgradeReplacementBinds() accepted missing execution state")
	}
	execution := &upgradeExecution{
		mutation: &boundMutation{preparation: Preparation{Workload: domain.DesiredWorkload{WorkloadSpec: domain.WorkloadSpec{
			Mounts: []domain.Mount{{
				Kind: domain.MountBind, Source: testBindSourceNew, Target: testVolumeTarget,
			}},
		}}}},
		sources: []backedStorageSource{{Mount: domain.RuntimeMount{
			Kind: domain.MountBind, Source: testBindSourceOld, Target: testVolumeTarget,
		}}},
	}
	if !errors.Is(prepareUpgradeReplacementBinds(execution), ErrInvalidRequest) {
		t.Fatal("prepareUpgradeReplacementBinds() accepted an empty backup root")
	}

	invalidMounts := []domain.Mount{{}, {Target: "/"}, {Target: "."}}
	for _, mount := range invalidMounts {
		if _, err := replacementBindPath("/backup", "transaction", mount); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("replacementBindPath(%#v) error = %v", mount, err)
		}
	}
	if _, err := replacementBindPath(
		"",
		"transaction",
		domain.Mount{Target: testVolumeTarget},
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("replacementBindPath(empty root) error = %v", err)
	}
}

//nolint:cyclop // Each case forces a distinct filesystem operation failure.
func TestEnsureEmptyReplacementBindFilesystemFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	parentFile := filepath.Join(root, "parent-file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(parent) error = %v", err)
	}
	if err := ensureEmptyReplacementBind(filepath.Join(parentFile, "child")); err == nil {
		t.Fatal("ensureEmptyReplacementBind() accepted a file parent")
	}
	if err := ensureEmptyReplacementBind(parentFile); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("ensureEmptyReplacementBind(file) error = %v", err)
	}

	unreadable := filepath.Join(root, "unreadable")
	if err := os.Mkdir(unreadable, 0o000); err != nil {
		t.Fatalf("Mkdir(unreadable) error = %v", err)
	}
	if err := ensureEmptyReplacementBind(unreadable); err == nil {
		t.Fatal("ensureEmptyReplacementBind() read an inaccessible directory")
	}
	if err := os.Chmod(unreadable, 0o700); err != nil { //nolint:gosec // Directories require execute permission.
		t.Fatalf("Chmod(unreadable) error = %v", err)
	}

	lockedParent := filepath.Join(root, "locked")
	if err := os.Mkdir(lockedParent, 0o500); err != nil {
		t.Fatalf("Mkdir(locked) error = %v", err)
	}
	if err := ensureEmptyReplacementBind(filepath.Join(lockedParent, "child")); err == nil {
		t.Fatal("ensureEmptyReplacementBind() created a child in a locked directory")
	}
	if err := os.Chmod(lockedParent, 0o700); err != nil { //nolint:gosec // Directories require execute permission.
		t.Fatalf("Chmod(locked) error = %v", err)
	}

	tooLong := filepath.Join(root, strings.Repeat("x", 4096))
	if err := ensureEmptyReplacementBind(tooLong); err == nil {
		t.Fatal("ensureEmptyReplacementBind() accepted an oversized path")
	}
}

func TestUpgradeCreateContainsReplacementBindFailure(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	fixture.mutation.preparation.Workload.Mounts = []domain.Mount{{
		Kind: domain.MountBind, Source: testBindSourceNew, Target: testVolumeTarget,
	}}
	if err := os.MkdirAll(fixture.mutation.backupRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(backup root) error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.mutation.backupRoot, replacementBindDirectory),
		[]byte("blocked"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(replacement blocker) error = %v", err)
	}
	execution := &upgradeExecution{
		mutation: fixture.mutation,
		runtime:  fixture.upgradeRuntime,
		sources: []backedStorageSource{{Mount: domain.RuntimeMount{
			Kind: domain.MountBind, Source: testBindSourceOld, Target: testVolumeTarget,
		}}},
	}
	if err := execution.create(context.Background()); err == nil {
		t.Fatal("upgrade create ignored replacement bind failure")
	}
}

func TestReplacementBindRejectsInvalidSelectedTarget(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	fixture.mutation.preparation.Workload.Mounts = []domain.Mount{{
		Kind: domain.MountBind, Source: testBindSourceNew, Target: "/",
	}}
	execution := &upgradeExecution{
		mutation: fixture.mutation,
		sources: []backedStorageSource{{Mount: domain.RuntimeMount{
			Kind: domain.MountBind, Source: testBindSourceOld, Target: "/",
		}}},
	}
	if err := prepareUpgradeReplacementBinds(execution); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("prepareUpgradeReplacementBinds(invalid target) error = %v", err)
	}
}
