package containerd

import (
	"context"
	"io"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

// ProbeWorkloadArchivePath validates one path in an exact transaction-owned
// root filesystem.
func (client *Client) ProbeWorkloadArchivePath(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	archivePath string,
) (application.ArchivePathStat, error) {
	identifier, err := client.archiveWorkload(ctx, workload, transaction, archivePath)
	if err != nil {
		return application.ArchivePathStat{}, err
	}
	var stat application.ArchivePathStat
	err = client.checked(ctx, func(requestContext context.Context) error {
		var err error
		stat, err = client.workloads.ArchiveStat(requestContext, identifier, archivePath)

		return wrapWorkloadBackendError("archive path probe", err)
	})

	return stat, workloadError(err)
}

// GetWorkloadArchive writes a bounded tar stream for one exact path.
func (client *Client) GetWorkloadArchive(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	archivePath string,
	destination io.Writer,
	maximumBytes int64,
) (application.ArchivePathStat, error) {
	if destination == nil || maximumBytes <= 0 {
		return application.ArchivePathStat{}, ErrProtocol
	}
	identifier, err := client.archiveWorkload(ctx, workload, transaction, archivePath)
	if err != nil {
		return application.ArchivePathStat{}, err
	}
	var stat application.ArchivePathStat
	err = client.checked(ctx, func(requestContext context.Context) error {
		var err error
		stat, err = client.workloads.ArchiveGet(
			requestContext, identifier, archivePath, destination, maximumBytes,
		)

		return wrapWorkloadBackendError("archive read", err)
	})

	return stat, workloadError(err)
}

// PutWorkloadArchive applies one tar stream to an exact path. Callers perform
// their own postcondition fetch and inventory comparison.
func (client *Client) PutWorkloadArchive(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	archivePath string,
	source io.Reader,
) error {
	if source == nil {
		return ErrProtocol
	}
	identifier, err := client.archiveWorkload(ctx, workload, transaction, archivePath)
	if err != nil {
		return err
	}
	err = client.checked(ctx, func(requestContext context.Context) error {
		return client.workloads.ArchivePut(requestContext, identifier, archivePath, source)
	})

	return workloadError(err)
}

func (client *Client) archiveWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	archivePath string,
) (string, error) {
	if client.CheckWorkload(workload) != nil || !validTransaction(transaction) ||
		!validArchivePath(archivePath) {
		return "", ErrUnsupportedWorkload
	}
	probe, err := client.probeWorkloadEffect(ctx, workload, transaction, "")
	if err != nil {
		return "", err
	}
	if probe.State != application.WorkloadEffectProbeObserved ||
		!validEffectWorkload(probe.Workload, workload, transaction) {
		return "", application.ErrArchiveConflict
	}

	return probe.Workload.ID, nil
}
