//go:build linux || darwin

package backup

import (
	"bytes"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestAnalyzeRequiresTerminatorsAfterZeroPayload(t *testing.T) {
	t.Parallel()
	archive := makeTar(t, regular("zero-filled", string(make([]byte, 1024))))
	for _, removed := range []int{512, 1024} {
		candidate := archive[:len(archive)-removed]
		inventory, err := Analyze(t.Context(), bytes.NewReader(candidate), int64(len(candidate)))
		if !errors.Is(err, ErrInvalidArchive) || inventory != (Inventory{}) {
			t.Fatalf("removed %d terminator bytes: inventory=%+v error=%v", removed, inventory, err)
		}
	}
	for _, padding := range []int{0, 512, 2048} {
		candidate := append(bytes.Clone(archive), make([]byte, padding)...)
		inventory, err := Analyze(t.Context(), bytes.NewReader(candidate), int64(len(candidate)))
		if err != nil || inventory.EntryCount != 1 || inventory.PayloadBytes != 1024 ||
			inventory.ArchiveDigest != domain.Hash(candidate) {
			t.Fatalf("padding %d: inventory=%+v error=%v", padding, inventory, err)
		}
	}
}

func TestAnalyzeRejectsUnalignedTrailingPadding(t *testing.T) {
	t.Parallel()
	archive := makeTar(t, regular("zero-filled", string(make([]byte, 1024))))
	archive = append(archive, 0)
	inventory, err := Analyze(t.Context(), bytes.NewReader(archive), int64(len(archive)))
	if !errors.Is(err, ErrInvalidArchive) || inventory != (Inventory{}) {
		t.Fatalf("unaligned archive accepted: %+v, %v", inventory, err)
	}
}

func TestPublishRejectsMissingTerminatorsEvenWithMatchingInventory(t *testing.T) {
	t.Parallel()
	archive := makeTar(t, regular("zero-filled", string(make([]byte, 1024))))
	inventory, err := Analyze(t.Context(), bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []int{512, 1024} {
		candidate := archive[:len(archive)-removed]
		manifest := validManifestForTest(t)
		manifest.Artifacts = manifest.Artifacts[:1]
		manifest.Artifacts[0].Inventory = inventory
		manifest.Artifacts[0].Inventory.ArchiveBytes = int64(len(candidate))
		manifest.Artifacts[0].Inventory.ArchiveDigest = domain.Hash(candidate)
		root := privatePublicationRoot(t)
		publication, err := publishCapacityChecked(t.Context(), t, root, manifest, []ArchiveInput{{
			Target: manifest.Artifacts[0].Mount.Target, Reader: bytes.NewReader(candidate), MaximumBytes: int64(len(candidate)),
		}})
		if !errors.Is(err, ErrInvalidArchive) || publication.ManifestPath != "" {
			t.Fatalf("Publish missing %d bytes = %+v, %v", removed, publication, err)
		}
		if _, found, err := Open(t.Context(), root, manifest.TransactionID); err != nil || found {
			t.Fatalf("invalid archive became visible: found=%t, error=%v", found, err)
		}
	}
}
