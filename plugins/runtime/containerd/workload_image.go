package containerd

import (
	"context"
	"encoding/json"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	"github.com/opencontainers/go-digest"
	imageidentity "github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

//nolint:cyclop // Snapshot selection proves the full image identity and every rootfs layer digest.
func (client *Client) snapshotParent(ctx context.Context, image domain.ImageIdentity) (string, error) {
	source, err := imageref.Normalize(image.Reference)
	if err != nil {
		return "", ErrProtocol
	}
	probe, err := client.ProbeImage(ctx, image)
	if err != nil {
		return "", err
	}
	if probe.State != application.ImageProbeObserved ||
		probe.Image.ReferenceDigest != image.ReferenceDigest ||
		probe.Image.PlatformManifest != image.PlatformManifest ||
		probe.Image.ImageConfig != image.ImageConfig || probe.Image.Platform != image.Platform {
		return "", ErrProtocol
	}

	var size int64
	err = client.checked(ctx, func(requestContext context.Context) error {
		response, infoErr := client.content.Info(requestContext, &contentapi.InfoRequest{
			Digest: image.ImageConfig.String(),
		})
		if infoErr != nil {
			return classifyRPCError(infoErr)
		}
		if response.GetInfo() == nil || response.GetInfo().GetDigest() != image.ImageConfig.String() ||
			response.GetInfo().GetSize() <= 0 || response.GetInfo().GetSize() > maximumMetadataBytes {
			return ErrProtocol
		}
		size = response.GetInfo().GetSize()

		return nil
	})
	if err != nil {
		return "", workloadError(err)
	}
	raw, err := (&imageRepository{client: client, source: source}).read(ctx, ocispec.Descriptor{
		Digest: digest.Digest(image.ImageConfig.String()), Size: size,
	}, true)
	if err != nil {
		return "", workloadError(err)
	}

	return snapshotChainID(raw)
}

func snapshotChainID(raw []byte) (string, error) {
	var configuration ocispec.Image
	if json.Unmarshal(raw, &configuration) != nil {
		return "", ErrProtocol
	}
	if len(configuration.RootFS.DiffIDs) == 0 {
		return "", nil
	}
	diffIDs := make([]digest.Digest, len(configuration.RootFS.DiffIDs))
	for index, selected := range configuration.RootFS.DiffIDs {
		if selected.Validate() != nil {
			return "", ErrProtocol
		}
		diffIDs[index] = selected
	}
	parent := imageidentity.ChainID(diffIDs)

	return parent.String(), nil
}
