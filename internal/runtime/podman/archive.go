package podman

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	archivePathQuery         = "path"
	podmanArchiveContentType = "application/x-tar"
)

// ProbeWorkloadArchivePath performs the native Libpod archive HEAD operation
// for one exact transaction-owned container.
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
	response, err := client.request(
		ctx,
		http.MethodHead,
		libpodPrefix+"/containers/"+container.ID+"/archive",
		url.Values{archivePathQuery: {archivePath}},
		nil,
		false,
	)
	if err != nil {
		return empty, err
	}
	defer closePodmanResponse(response)

	if response.StatusCode == http.StatusNotFound {
		return empty, application.ErrArchivePathMissing
	}
	if response.StatusCode != http.StatusOK {
		return empty, ErrProtocol
	}

	stat, err := decodePodmanArchivePathStat(response.Header.Get("X-Docker-Container-Path-Stat"))
	if err != nil || stat.Name != path.Base(archivePath) {
		return empty, ErrProtocol
	}

	return stat, nil
}

// GetWorkloadArchive streams one validated native Libpod tar response to the
// destination. The path-stat header is validated before the first write.
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
	response, err := client.requestWithReader(
		ctx,
		http.MethodGet,
		libpodPrefix+"/containers/"+container.ID+"/archive",
		url.Values{archivePathQuery: {archivePath}},
		http.Header{"Accept-Encoding": {"identity"}},
		nil,
		true,
	)
	if err != nil {
		return empty, err
	}
	stat, err := validatePodmanArchiveResponse(response, archivePath)
	if err != nil {
		return empty, err
	}
	if err = copyPodmanArchive(response, destination, maximumBytes); err != nil {
		return empty, err
	}
	if !client.archiveSocketStable() {
		return empty, ErrInvalidEndpoint
	}

	return stat, nil
}

// PutWorkloadArchive extracts one tar stream into the exact native Libpod
// container path. The success response remains transport evidence only.
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
	response, err := client.requestWithReader(
		ctx,
		http.MethodPut,
		libpodPrefix+"/containers/"+container.ID+"/archive",
		url.Values{
			archivePathQuery:       {archivePath},
			"noOverwriteDirNonDir": {podmanQueryTrue},
			"copyUIDGID":           {podmanQueryTrue},
		},
		http.Header{podmanContentType: {podmanArchiveContentType}},
		source,
		true,
	)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return podmanArchiveResponseError(response)
	}

	return decodePodmanEmptyResponse(response, http.StatusOK)
}

func (client *Client) archiveContainer(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (Container, error) {
	var empty Container

	if client == nil || client.CheckWorkload(workload) != nil || !validOwnershipName(transaction) {
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

func (client *Client) archiveSocketStable() bool {
	identity, err := inspectSocket(client.socketPath)

	return err == nil && identity == client.socket
}
