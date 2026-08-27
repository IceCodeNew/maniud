package containerd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	containertypes "github.com/containerd/containerd/api/types"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
)

type imageRepository struct {
	client *Client
	source imageref.Source
}

var _ registry.LocalRepository = (*imageRepository)(nil)

// ResolveLocalImage connects to one explicit containerd namespace, verifies
// its local image graph, and closes the connection before returning.
func ResolveLocalImage(
	ctx context.Context,
	address string,
	namespace string,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	client, err := Connect(ctx, address, namespace)
	if err != nil {
		return domain.ImageIdentity{}, registryTransportError(err)
	}
	image, resolveErr := client.Resolve(ctx, source, platform)

	return localImageResult(image, resolveErr, client.Close())
}

func localImageResult(
	image domain.ImageIdentity,
	resolveErr error,
	closeErr error,
) (domain.ImageIdentity, error) {
	if resolveErr != nil {
		return domain.ImageIdentity{}, resolveErr
	}
	if closeErr != nil {
		return domain.ImageIdentity{}, registry.ErrUnavailable
	}

	return image, nil
}

// Resolve verifies one namespace-local image record and its complete selected
// OCI content graph. Missing local content is terminal because containerd's
// core API does not provide a registry pull operation.
func (client *Client) Resolve(
	ctx context.Context,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	if client == nil || client.images == nil || client.content == nil {
		return domain.ImageIdentity{}, registry.ErrUnavailable
	}

	image, err := registry.ResolveLocal(
		ctx,
		source,
		platform,
		&imageRepository{client: client, source: source},
	)
	if err != nil {
		return domain.ImageIdentity{}, fmt.Errorf("resolve containerd image: %w", err)
	}

	return image, nil
}

func (repository *imageRepository) FetchReference(
	ctx context.Context,
	_ string,
) (ocispec.Descriptor, io.ReadCloser, error) {
	var target ocispec.Descriptor
	err := repository.client.checked(ctx, func(requestContext context.Context) error {
		image, imageErr := localImageRecord(requestContext, repository.client.images, repository.source)
		if imageErr != nil {
			return imageErr
		}
		target = apiDescriptor(image.GetTarget())

		return nil
	})
	if err != nil {
		return ocispec.Descriptor{}, nil, registryTransportError(err)
	}

	reader, err := repository.Fetch(ctx, target)

	return target, reader, err
}

func containerdImageNames(source imageref.Source) []string {
	exact := source.String()
	tagged, _, pinned := strings.Cut(exact, "@")
	if !pinned {
		return []string{exact}
	}

	return []string{exact, tagged}
}

func localImageRecord(
	ctx context.Context,
	images imagesClient,
	source imageref.Source,
) (*imagesapi.Image, error) {
	if images == nil {
		return nil, ErrUnavailable
	}
	for _, name := range containerdImageNames(source) {
		response, err := images.Get(ctx, &imagesapi.GetImageRequest{Name: name})
		if status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return nil, classifyRPCError(err)
		}
		image := response.GetImage()
		if image == nil || image.GetName() != name || image.GetTarget() == nil {
			return nil, ErrProtocol
		}

		return image, nil
	}

	return nil, errNotFound
}

func (repository *imageRepository) Fetch(
	ctx context.Context,
	target ocispec.Descriptor,
) (io.ReadCloser, error) {
	raw, err := repository.read(ctx, target, true)
	if err != nil {
		return nil, err
	}

	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (repository *imageRepository) Verify(ctx context.Context, target ocispec.Descriptor) error {
	_, err := repository.read(ctx, target, false)

	return err
}

func (repository *imageRepository) read(
	ctx context.Context,
	target ocispec.Descriptor,
	capture bool,
) ([]byte, error) {
	expected, err := validContentDescriptor(target, capture)
	if err != nil {
		return nil, err
	}

	var raw []byte
	err = repository.client.checked(ctx, func(requestContext context.Context) error {
		contentInfo, err := repository.client.content.Info(requestContext, &contentapi.InfoRequest{
			Digest: target.Digest.String(),
		})
		if err != nil {
			return classifyRPCError(err)
		}
		if !matchesContentInfo(contentInfo, target) {
			return ErrProtocol
		}

		return nil
	})
	if err != nil {
		return nil, registryTransportError(err)
	}

	err = repository.client.checked(ctx, func(requestContext context.Context) error {
		stream, err := repository.client.content.Read(requestContext, &contentapi.ReadContentRequest{
			Digest: target.Digest.String(), Offset: 0, Size: target.Size,
		})
		if err != nil {
			return classifyRPCError(err)
		}
		raw, err = consumeContent(stream, target.Size, expected, capture)

		return err
	})
	if err != nil {
		return nil, registryTransportError(err)
	}

	return raw, nil
}

func validContentDescriptor(target ocispec.Descriptor, capture bool) (domain.Digest, error) {
	expected, err := domain.ParseDigest(target.Digest.String())
	if err != nil || target.Size <= 0 || capture && target.Size > maximumMetadataBytes {
		return domain.Digest{}, registry.ErrProtocol
	}

	return expected, nil
}

func matchesContentInfo(response *contentapi.InfoResponse, target ocispec.Descriptor) bool {
	info := response.GetInfo()

	return info != nil && info.GetDigest() == target.Digest.String() && info.GetSize() == target.Size
}

func consumeContent(
	stream contentapi.Content_ReadClient,
	size int64,
	expected domain.Digest,
	capture bool,
) ([]byte, error) {
	if stream == nil {
		return nil, ErrProtocol
	}
	hash := sha256.New()
	var output bytes.Buffer
	if capture {
		output.Grow(int(size))
	}
	observed := int64(0)
	for chunks := 0; ; chunks++ {
		if chunks >= maximumContentChunks {
			return nil, ErrProtocol
		}
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, classifyRPCError(err)
		}
		observed, err = consumeContentChunk(response, observed, size, hash, &output, capture)
		if err != nil {
			return nil, ErrProtocol
		}
	}
	if observed != size || !bytes.Equal(hash.Sum(nil), expected[:]) {
		return nil, ErrProtocol
	}

	return output.Bytes(), nil
}

func consumeContentChunk(
	response *contentapi.ReadContentResponse,
	observed int64,
	size int64,
	digester hash.Hash,
	output *bytes.Buffer,
	capture bool,
) (int64, error) {
	data := response.GetData()
	if response.GetOffset() != observed || int64(len(data)) > size-observed {
		return 0, ErrProtocol
	}
	_, _ = digester.Write(data)
	if capture {
		_, _ = output.Write(data)
	}

	return observed + int64(len(data)), nil
}

func apiDescriptor(value *containertypes.Descriptor) ocispec.Descriptor {
	if value == nil {
		return ocispec.Descriptor{}
	}

	return ocispec.Descriptor{
		MediaType: value.GetMediaType(), Digest: digest.Digest(value.GetDigest()), Size: value.GetSize(),
		URLs: nil, Annotations: value.GetAnnotations(), Data: nil, Platform: nil, ArtifactType: "",
	}
}

func classifyRPCError(err error) error {
	if errors.Is(err, context.Canceled) {
		return errCancelled
	}

	switch status.Code(err) {
	case codes.Canceled:
		return errCancelled
	case codes.NotFound:
		return errNotFound
	case codes.PermissionDenied, codes.Unauthenticated:
		return errUnauthorized
	case codes.ResourceExhausted:
		return errRateLimited
	case codes.DeadlineExceeded, codes.Unavailable:
		return ErrUnavailable
	case codes.OK, codes.Unknown, codes.InvalidArgument, codes.AlreadyExists,
		codes.FailedPrecondition, codes.Aborted, codes.OutOfRange, codes.Unimplemented,
		codes.Internal, codes.DataLoss:
		return ErrProtocol
	default:
		return ErrProtocol
	}
}

func registryTransportError(err error) error {
	switch {
	case errors.Is(err, errCancelled):
		return registry.ErrCancelled
	case errors.Is(err, errNotFound):
		return registry.ErrNotFound
	case errors.Is(err, errUnauthorized):
		return registry.ErrUnauthorized
	case errors.Is(err, errRateLimited):
		return registry.ErrRateLimited
	case errors.Is(err, ErrUnavailable):
		return registry.ErrUnavailable
	default:
		return registry.ErrProtocol
	}
}
