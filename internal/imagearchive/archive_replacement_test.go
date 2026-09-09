package imagearchive

import (
	"errors"
	"os"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestAnalyzeRejectsSourceReplacedAfterOpen(t *testing.T) {
	t.Parallel()
	path := writeMinimalInternalArchive(t)
	source := Source{path: path, selector: "@0"}
	if _, err := Analyze(t.Context(), source); err != nil {
		t.Fatalf("unchanged archive failed: %v", err)
	}
	//nolint:gosec // Read the archive created by this test under t.TempDir.
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := analyzeWithOperations(t.Context(), source, analyzeOperations{
		open: func(name string) (*os.File, fileIdentity, error) {
			file, before, openErr := openSource(name)
			if openErr != nil {
				return file, before, openErr
			}
			t.Cleanup(func() { _ = file.Close() })
			if renameErr := os.Rename(name, name+".original"); renameErr != nil {
				t.Fatal(renameErr)
			}
			//nolint:gosec // Replace only the test-owned source path passed to this opener.
			if writeErr := os.WriteFile(name, contents, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}

			return file, before, nil
		},
		close: (*os.File).Close,
	})
	if !errors.Is(err, ErrInvalidSource) || analysis.ArchiveDigest != (domain.Digest{}) {
		t.Fatalf("replaced source produced archive proof %#v, error %v", analysis, err)
	}
}
