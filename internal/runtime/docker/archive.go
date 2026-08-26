package docker

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const archivePathQuery = "path"

// ProbeWorkloadArchivePath performs the Docker archive HEAD operation for one
// exact transaction-owned container and validates its path-stat header.
func (client *Client) ProbeWorkloadArchivePath(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	archivePath string,
) (application.ArchivePathStat, error) {
	var empty application.ArchivePathStat

	if !validArchivePath(archivePath) {
		return empty, ErrProtocol
	}
	container, err := client.archiveContainer(ctx, workload, transaction)
	if err != nil {
		return empty, err
	}
	pathValue, _ := client.apiPath("/containers/" + container.ID + "/archive")

	response, err := client.archiveRequest(ctx, http.MethodHead, pathValue, url.Values{
		archivePathQuery: {archivePath},
	}, nil)
	if err != nil {
		return empty, err
	}
	defer closeResponse(response)

	if response.StatusCode == http.StatusNotFound {
		return empty, application.ErrArchivePathMissing
	}
	if response.StatusCode != http.StatusOK {
		return empty, ErrProtocol
	}

	stat, err := decodeDockerArchivePathStat(response.Header.Get("X-Docker-Container-Path-Stat"))
	if err != nil || stat.Name != path.Base(archivePath) {
		return empty, ErrProtocol
	}

	return stat, nil
}

// GetWorkloadArchive streams one validated Docker tar response to destination.
// The response stat is validated before any archive bytes are written.
func (client *Client) GetWorkloadArchive(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	archivePath string,
	destination io.Writer,
	maximumBytes int64,
) (application.ArchivePathStat, error) {
	var empty application.ArchivePathStat

	if destination == nil || maximumBytes <= 0 || !validArchivePath(archivePath) {
		return empty, ErrProtocol
	}
	container, err := client.archiveContainer(ctx, workload, transaction)
	if err != nil {
		return empty, err
	}
	pathValue, _ := client.apiPath("/containers/" + container.ID + "/archive")
	response, err := client.archiveRequest(ctx, http.MethodGet, pathValue, url.Values{
		archivePathQuery: {archivePath},
	}, nil)
	if err != nil {
		return empty, err
	}

	stat, err := validateDockerArchiveResponse(response, archivePath)
	if err != nil {
		return empty, err
	}
	if err = copyDockerArchive(response, destination, maximumBytes); err != nil {
		return empty, err
	}

	return stat, nil
}

// PutWorkloadArchive extracts one tar stream into the exact container path.
// The successful HTTP response is deliberately not treated as restore proof.
func (client *Client) PutWorkloadArchive(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	archivePath string,
	source io.Reader,
) error {
	if source == nil || !validArchivePath(archivePath) {
		return ErrProtocol
	}
	container, err := client.archiveContainer(ctx, workload, transaction)
	if err != nil {
		return err
	}
	pathValue, _ := client.apiPath("/containers/" + container.ID + "/archive")
	response, err := client.archiveRequest(ctx, http.MethodPut, pathValue, url.Values{
		"path":                 {archivePath},
		"noOverwriteDirNonDir": {dockerQueryTrue},
		"copyUIDGID":           {dockerQueryTrue},
	}, source)
	if err != nil {
		return err
	}

	return decodeDockerArchivePutResponse(response)
}

func (client *Client) archiveContainer(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (Container, error) {
	var empty Container

	if client == nil || client.CheckWorkload(workload) != nil || !validTransaction(transaction) {
		return empty, ErrUnsupportedWorkload
	}
	probe, err := client.probeOwnedContainer(ctx, workload.ServiceName, transaction)
	if err != nil {
		return empty, err
	}
	if probe.State != ContainerProbeObserved || !validArchiveContainer(probe.Container, workload, transaction) {
		return empty, application.ErrArchiveConflict
	}

	return probe.Container, nil
}

func validArchiveContainer(container Container, workload domain.DesiredWorkload, transaction string) bool {
	return container.Ownership.Status == domain.OwnershipManaged &&
		container.Ownership.Service == workload.ServiceName &&
		container.Ownership.Transaction == transaction &&
		container.Ownership.DesiredState != (domain.Digest{}) &&
		container.Ownership.Reference != (domain.Digest{}) &&
		container.Ownership.ImageConfig != (domain.Digest{}) &&
		container.Ownership.PlatformManifest != (domain.Digest{})
}
