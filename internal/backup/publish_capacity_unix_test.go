//go:build linux || darwin

package backup

import (
	"context"
	"errors"
	"testing"
)

func TestPublishRevalidatesCapacityEvidence(t *testing.T) {
	t.Parallel()

	root := privatePublicationRoot(t)
	manifest := validManifestForTest(t)
	capacity, err := PreparePublicationCapacity(root, manifest)
	if err != nil {
		t.Fatalf("PreparePublicationCapacity() error = %v", err)
	}
	result, err := Publish(
		context.Background(),
		root,
		manifest,
		publicationArchives(t, manifest, 0),
		capacity,
	)
	if err != nil || result.Manifest.TransactionID != manifest.TransactionID {
		t.Fatalf("Publish() = %#v, %v", result, err)
	}

	drifted := capacity
	drifted.filesystem.filesystemID[0]++
	_, err = Publish(
		context.Background(),
		root,
		manifest,
		publicationArchives(t, manifest, 0),
		drifted,
	)
	if !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("Publish(drifted filesystem) error = %v", err)
	}
}

func TestPublishRejectsDriftedCapacityEvidence(t *testing.T) {
	t.Parallel()

	root := privatePublicationRoot(t)
	manifest := validManifestForTest(t)
	capacity, err := PreparePublicationCapacity(root, manifest)
	if err != nil {
		t.Fatalf("PreparePublicationCapacity() error = %v", err)
	}

	insufficient := capacity
	insufficient.requiredBytes = ^uint64(0)
	_, err = Publish(
		context.Background(),
		root,
		manifest,
		publicationArchives(t, manifest, 0),
		insufficient,
	)
	if !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("Publish(insufficient capacity) error = %v", err)
	}

	manifest.TransactionID = Identifier{4}
	_, err = Publish(
		context.Background(),
		root,
		manifest,
		publicationArchives(t, manifest, 0),
		capacity,
	)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Publish(drifted manifest) error = %v", err)
	}
}

func TestPublishValidatesContextAndRootBeforeCapacity(t *testing.T) {
	t.Parallel()

	root := privatePublicationRoot(t)
	manifest := validManifestForTest(t)
	capacity, err := PreparePublicationCapacity(root, manifest)
	if err != nil {
		t.Fatalf("PreparePublicationCapacity() error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Publish(
		cancelled,
		root,
		manifest,
		publicationArchives(t, manifest, 0),
		capacity,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish(cancelled) error = %v", err)
	}
	_, err = Publish(
		context.Background(),
		"relative",
		manifest,
		publicationArchives(t, manifest, 0),
		capacity,
	)
	if !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("Publish(invalid root) error = %v", err)
	}
}
