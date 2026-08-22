//go:build linux || darwin

package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

//nolint:cyclop,funlen // The table keeps publication content failures in one audit surface.
func TestOpenPublicationRejectsContentBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*testing.T, string, Manifest)
		want   error
	}{
		{
			name:   "broad publication directory",
			mutate: broadenPublicationDirectory,
			want:   ErrInvalidArchive,
		},
		{
			name: "publication directory symlink",
			mutate: func(t *testing.T, root string, manifest Manifest) {
				t.Helper()
				path := publicationPath(root, manifest)
				target := path + ".target"
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(target), path); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrInvalidArchive,
		},
		{
			name: "unreadable manifest",
			mutate: func(t *testing.T, root string, manifest Manifest) {
				t.Helper()
				//nolint:gosec // Unsafe mode is the rejected fixture.
				if err := os.Chmod(
					publicationManifestPath(root, manifest), 0o644,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrInvalidArchive,
		},
		{
			name: "malformed manifest",
			mutate: func(t *testing.T, root string, manifest Manifest) {
				t.Helper()
				if err := os.WriteFile(publicationManifestPath(root, manifest), []byte("{"), privateFileMode); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrInvalidManifest,
		},
		{
			name: "wrong transaction",
			mutate: func(t *testing.T, root string, manifest Manifest) {
				t.Helper()
				path := publicationManifestPath(root, manifest)
				manifest.TransactionID = Identifier{9}
				raw, _, err := EncodeManifest(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, privateFileMode); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrInvalidManifest,
		},
		{
			name: "extra entry",
			mutate: func(t *testing.T, root string, manifest Manifest) {
				t.Helper()
				path := filepath.Join(root, manifest.TransactionID.String(), "extra")
				if err := os.WriteFile(path, []byte("x"), privateFileMode); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrInvalidArchive,
		},
		{
			name: "changed artifact",
			mutate: func(t *testing.T, root string, manifest Manifest) {
				t.Helper()
				path := filepath.Join(root, manifest.TransactionID.String(), manifest.Artifacts[0].FileName)
				if err := os.WriteFile(path, []byte("changed"), privateFileMode); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrInvalidArchive,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root, manifest := publishedOpenFixture(t)
			test.mutate(t, root, manifest)
			if _, found, err := Open(context.Background(), root, manifest.TransactionID); found || !errors.Is(err, test.want) {
				t.Fatalf("Open() = %t, %v", found, err)
			}
		})
	}
}

func TestOpenPublicationDescriptorBoundaries(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	root, found, err := existingPublicationRoot(cancelled, "/unused", Identifier{1})
	if root != nil || found || !errors.Is(err, context.Canceled) {
		t.Fatalf("existingPublicationRoot(cancelled) = %#v, %t, %v", root, found, err)
	}

	t.Run("changed root", func(t *testing.T) {
		t.Parallel()

		rootPath, manifest := publishedOpenFixture(t)
		root, directory := openPublicationDescriptors(t, rootPath, manifest)
		moved := rootPath + ".moved"
		if err := os.Rename(rootPath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(rootPath, privateDirectoryMode); err != nil {
			t.Fatal(err)
		}
		if _, found, err := readPublication(
			context.Background(), root, directory, manifest.TransactionID, defaultPublicationOperations(),
		); found || !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("readPublication(changed root) = %t, %v", found, err)
		}
		_ = os.Remove(rootPath)
		_ = os.Rename(moved, rootPath)
	})

	t.Run("close failure", func(t *testing.T) {
		t.Parallel()

		rootPath, manifest := publishedOpenFixture(t)
		root, directory := openPublicationDescriptors(t, rootPath, manifest)
		operations := defaultPublicationOperations()
		baseClose := operations.closeDescriptor
		operations.closeDescriptor = func(descriptor int) error {
			return errors.Join(baseClose(descriptor), errPublicationTest)
		}
		if _, found, err := readPublication(
			context.Background(), root, directory, manifest.TransactionID, operations,
		); found || err == nil {
			t.Fatalf("readPublication(close failure) = %t, %v", found, err)
		}
	})
}

func publishedOpenFixture(t *testing.T) (string, Manifest) {
	t.Helper()

	root := privatePublicationRoot(t)
	manifest := validManifestForTest(t)
	if _, err := publishCapacityChecked(
		context.Background(), t, root, manifest, publicationArchives(t, manifest, 0),
	); err != nil {
		t.Fatal(err)
	}

	return root, manifest
}

func publicationManifestPath(root string, manifest Manifest) string {
	return filepath.Join(root, manifest.TransactionID.String(), manifestName)
}

func openPublicationDescriptors(
	t *testing.T,
	rootPath string,
	manifest Manifest,
) (*directoryAnchor, int) {
	t.Helper()

	root, found := openExistingBackupRoot(rootPath)
	if !found {
		t.Fatal("openExistingBackupRoot() = false")
	}
	directory, _, err := openPublicationDirectory(root, manifest.TransactionID.String())
	if err != nil {
		t.Fatalf("openPublicationDirectory() error = %v", err)
	}

	return root, directory
}
