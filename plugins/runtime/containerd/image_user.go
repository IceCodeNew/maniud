package containerd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/imagerootfs"
)

const maximumImageUserLayers = 1 << 16

// ResolveImageUser resolves a named or incomplete image user against the
// verified final image filesystem without creating a snapshot or container.
func (client *Client) ResolveImageUser(
	ctx context.Context,
	expected domain.ImageIdentity,
	specification string,
) (string, error) {
	source, valid := normalizedContainerdImage(expected)
	if client == nil || !valid {
		return "", ErrUnsupportedWorkload
	}
	resolved, err := client.Resolve(ctx, source, expected.Platform)
	if err != nil || !sameContainerdImage(resolved, expected) {
		return "", ErrProtocol
	}
	layers, err := client.imageUserLayers(ctx, source, expected)
	if err != nil {
		return "", err
	}
	user, err := imagerootfs.ResolveCompressedUser(ctx, layers, specification)
	if err != nil {
		return "", ErrProtocol
	}

	return user, nil
}

func sameContainerdImage(left, right domain.ImageIdentity) bool {
	return left.Origin == right.Origin && left.Reference == right.Reference &&
		left.ReferenceDigest == right.ReferenceDigest && left.Platform == right.Platform &&
		left.PlatformManifest == right.PlatformManifest && left.ImageConfig == right.ImageConfig
}

func (client *Client) imageUserLayers(
	ctx context.Context,
	source imageref.Source,
	expected domain.ImageIdentity,
) ([]imagerootfs.CompressedLayer, error) {
	manifestDescriptor, err := client.contentDescriptor(ctx, expected.PlatformManifest)
	if err != nil {
		return nil, err
	}
	repository := &imageRepository{client: client, source: source}
	raw, err := repository.read(ctx, manifestDescriptor, true)
	if err != nil {
		return nil, err
	}
	manifest, valid := decodeImageUserManifest(raw, expected)
	if !valid {
		return nil, ErrProtocol
	}

	layers := make([]imagerootfs.CompressedLayer, len(manifest.Layers))
	for index, descriptor := range manifest.Layers {
		target := descriptor
		if _, err = validContentDescriptor(target, false); err != nil ||
			len(target.URLs) != 0 || len(target.Data) != 0 {
			return nil, ErrProtocol
		}
		layers[index] = imagerootfs.CompressedLayer{
			Digest: target.Digest.String(), Size: target.Size, MediaType: target.MediaType,
			Open: func() (io.ReadCloser, error) {
				return client.openVerifiedContent(ctx, target)
			},
		}
	}

	return layers, nil
}

func (client *Client) contentDescriptor(
	ctx context.Context,
	identity domain.Digest,
) (ocispec.Descriptor, error) {
	target := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.Digest(identity.String()),
	}
	err := client.checked(ctx, func(requestContext context.Context) error {
		response, infoErr := client.content.Info(requestContext, &contentapi.InfoRequest{
			Digest: identity.String(),
		})
		if infoErr != nil {
			return classifyRPCError(infoErr)
		}
		if response.GetInfo() == nil || response.GetInfo().GetDigest() != identity.String() ||
			response.GetInfo().GetSize() <= 0 || response.GetInfo().GetSize() > maximumMetadataBytes {
			return ErrProtocol
		}
		target.Size = response.GetInfo().GetSize()

		return nil
	})
	if err != nil {
		return ocispec.Descriptor{}, workloadError(err)
	}

	return target, nil
}

func decodeImageUserManifest(raw []byte, expected domain.ImageIdentity) (ocispec.Manifest, bool) {
	var manifest ocispec.Manifest
	if json.Unmarshal(raw, &manifest) != nil || !validImageUserManifest(manifest, expected) {
		return ocispec.Manifest{}, false
	}
	for _, layer := range manifest.Layers {
		if layer.Digest.Validate() != nil || layer.Size <= 0 || layer.MediaType == "" {
			return ocispec.Manifest{}, false
		}
	}

	return manifest, true
}

func validImageUserManifest(manifest ocispec.Manifest, expected domain.ImageIdentity) bool {
	return manifest.SchemaVersion == 2 && manifest.ArtifactType == "" && manifest.Subject == nil &&
		len(manifest.Layers) <= maximumImageUserLayers &&
		manifest.Config.Digest.String() == expected.ImageConfig.String() &&
		manifest.Config.Size > 0 && manifest.Config.Size <= maximumMetadataBytes
}

func (client *Client) openVerifiedContent(
	ctx context.Context,
	target ocispec.Descriptor,
) (io.ReadCloser, error) {
	if client == nil || client.content == nil {
		return nil, ErrUnavailable
	}
	if _, err := validContentDescriptor(target, false); err != nil {
		return nil, ErrProtocol
	}
	reader, writer := io.Pipe()
	go func() {
		err := client.checked(ctx, func(requestContext context.Context) error {
			return client.copyVerifiedContent(requestContext, target, writer)
		})
		_ = writer.CloseWithError(workloadError(err))
	}()

	return reader, nil
}

func (client *Client) copyVerifiedContent(
	ctx context.Context,
	target ocispec.Descriptor,
	destination io.Writer,
) error {
	response, err := client.content.Info(ctx, &contentapi.InfoRequest{Digest: target.Digest.String()})
	if err != nil {
		return classifyRPCError(err)
	}
	if !matchesContentInfo(response, target) {
		return ErrProtocol
	}
	stream, err := client.content.Read(ctx, &contentapi.ReadContentRequest{
		Digest: target.Digest.String(), Offset: 0, Size: target.Size,
	})
	if err != nil {
		return classifyRPCError(err)
	}

	digester := sha256.New()
	observed, err := receiveVerifiedContent(stream, target.Size, digester, destination)
	if err != nil {
		return err
	}
	expected, _ := domain.ParseDigest(target.Digest.String())
	if observed != target.Size || !bytes.Equal(digester.Sum(nil), expected[:]) {
		return ErrProtocol
	}

	return nil
}

func receiveVerifiedContent(
	stream contentapi.Content_ReadClient,
	size int64,
	digester hash.Hash,
	destination io.Writer,
) (int64, error) {
	observed := int64(0)
	for chunks := 0; ; chunks++ {
		if chunks == maximumContentChunks {
			return 0, ErrProtocol
		}
		chunk, readErr := stream.Recv()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, classifyRPCError(readErr)
		}
		var err error
		observed, err = writeVerifiedContentChunk(chunk, observed, size, digester, destination)
		if err != nil {
			return 0, err
		}
	}

	return observed, nil
}

func writeVerifiedContentChunk(
	response *contentapi.ReadContentResponse,
	observed int64,
	size int64,
	digester hash.Hash,
	destination io.Writer,
) (int64, error) {
	data := response.GetData()
	if response == nil || len(data) == 0 || response.GetOffset() != observed || int64(len(data)) > size-observed {
		return 0, ErrProtocol
	}
	_, _ = digester.Write(data)
	written, err := destination.Write(data)
	if err != nil {
		return 0, fmt.Errorf("write verified containerd content: %w", err)
	}
	if written != len(data) {
		return 0, io.ErrShortWrite
	}

	return observed + int64(len(data)), nil
}
