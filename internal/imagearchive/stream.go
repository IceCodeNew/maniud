package imagearchive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	savedArchiveName = "image.tar"
	savedArchiveMode = os.FileMode(0o600)
)

type streamSpool struct {
	directory string
	path      string
	root      *os.Root
	file      *os.File
}

// AnalyzeStream validates a bounded Docker save stream containing exactly one
// selected image. The stream is spooled into an owner-private temporary
// directory so the regular-file archive validator remains the sole parser.
func AnalyzeStream(ctx context.Context, input io.Reader) (analysis Analysis, returnErr error) {
	if input == nil {
		return Analysis{}, ErrInvalidArchive
	}
	if err := ctx.Err(); err != nil {
		return Analysis{}, fmt.Errorf("analyze docker archive stream: %w", err)
	}

	spool, err := createStreamSpool()
	if err != nil {
		return Analysis{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, spool.root.Close(), removeSpool(spool.directory))
	}()
	if err := writeStreamSpool(ctx, input, spool.file); err != nil {
		return Analysis{}, err
	}

	source := Source{path: spool.path, selector: "@0", strictSingle: true}

	return Analyze(ctx, source)
}

func createStreamSpool() (streamSpool, error) {
	return createStreamSpoolWithMkdirTemp(os.MkdirTemp)
}

func createStreamSpoolWithMkdirTemp(
	mkdirTemp func(string, string) (string, error),
) (streamSpool, error) {
	directory, err := mkdirTemp("", "maniud-image-save-")
	if err != nil {
		return streamSpool{}, fmt.Errorf("create Docker archive spool: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return streamSpool{}, errors.Join(
			fmt.Errorf("open Docker archive spool: %w", err),
			removeSpool(directory),
		)
	}
	file, err := root.OpenFile(savedArchiveName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, savedArchiveMode)
	if err != nil {
		return streamSpool{}, errors.Join(
			fmt.Errorf("create Docker archive spool file: %w", err),
			root.Close(),
			removeSpool(directory),
		)
	}

	return streamSpool{
		directory: directory,
		path:      filepath.Join(directory, savedArchiveName),
		root:      root,
		file:      file,
	}, nil
}

func writeStreamSpool(ctx context.Context, input io.Reader, file *os.File) error {
	limited := &io.LimitedReader{R: contextReader{check: ctx.Err, reader: input}, N: maximumArchiveBytes + 1}
	written, copyErr := io.CopyBuffer(file, limited, make([]byte, archiveReadBufferBytes))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf(
			"write Docker archive spool: %w",
			errors.Join(copyErr, syncErr, closeErr),
		)
	}
	if written <= 0 || written > maximumArchiveBytes {
		return ErrInvalidArchive
	}

	return nil
}

func removeSpool(directory string) error {
	err := os.RemoveAll(directory)
	if err != nil {
		return fmt.Errorf("remove Docker archive spool: %w", err)
	}

	return nil
}
