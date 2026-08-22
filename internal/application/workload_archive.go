package application

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
)

var (
	// ErrArchivePathMissing reports that the exact container path was absent.
	ErrArchivePathMissing = errors.New("workload archive path is missing")
	// ErrArchiveConflict reports that runtime ownership or archive evidence
	// conflicts with the requested workload.
	ErrArchiveConflict = errors.New("workload archive evidence conflicts")
)

// ArchivePathStat is the runtime-neutral stat returned by a container archive
// endpoint. The adapter must prove this metadata before exposing an archive
// stream to backup or restore code. Name is the final path component reported
// by the runtime, and Mode uses the standard library os.FileMode bit layout.
type ArchivePathStat struct {
	Name       string
	Size       int64
	Mode       os.FileMode
	ModTime    time.Time
	LinkTarget string
}

// WorkloadArchiveRuntime transfers one exact transaction-owned workload path.
// A successful PutWorkloadArchive only proves the HTTP/API operation returned
// success. Callers must fetch and validate the restored path independently.
type WorkloadArchiveRuntime interface {
	ProbeWorkloadArchivePath(
		ctx context.Context,
		workload domain.DesiredWorkload,
		transaction string,
		path string,
	) (ArchivePathStat, error)
	GetWorkloadArchive(
		ctx context.Context,
		workload domain.DesiredWorkload,
		transaction string,
		path string,
		destination io.Writer,
		maximumBytes int64,
	) (ArchivePathStat, error)
	PutWorkloadArchive(
		ctx context.Context,
		workload domain.DesiredWorkload,
		transaction string,
		path string,
		source io.Reader,
	) error
}
