// Package imagerootfs reads bounded files from the final filesystem view of a
// verified container image without materializing that filesystem on disk.
package imagerootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	userdb "github.com/moby/sys/user"
)

const (
	passwdPath             = "etc/passwd"
	groupPath              = "etc/group"
	accountFileCount       = 2
	maximumAccountFileSize = int64(1 << 20)
	maximumRootFSEntries   = 1_000_000
)

var (
	// ErrInvalidImage reports a malformed or unsupported image filesystem.
	ErrInvalidImage = errors.New("image filesystem is invalid")
	// ErrUnknownUser reports an image user that cannot be resolved to UID:GID.
	ErrUnknownUser = errors.New("image user is not present in the image account database")
)

// CompressedLayer describes one ordered, digest-addressed image layer. Open
// must return a fresh, integrity-verifying compressed stream on every call.
type CompressedLayer struct {
	Digest    string
	Size      int64
	MediaType string
	Open      func() (io.ReadCloser, error)
}

// ResolveArchiveUser resolves a user against one validated, single-image
// Docker save archive.
func ResolveArchiveUser(ctx context.Context, path, specification string) (string, error) {
	image, err := tarball.ImageFromPath(path, nil)
	if err != nil {
		return "", ErrInvalidImage
	}

	return ResolveUser(ctx, image, specification)
}

// ResolveCompressedUser resolves a user against ordered compressed OCI or
// Docker layers supplied by a runtime adapter.
func ResolveCompressedUser(
	ctx context.Context,
	layers []CompressedLayer,
	specification string,
) (string, error) {
	imageLayers := make([]v1.Layer, len(layers))
	for index, layer := range layers {
		converted, err := compressedLayerValue(layer)
		if err != nil {
			return "", fmt.Errorf("convert compressed image layer: %w", err)
		}
		imageLayers[index] = converted
	}

	return ResolveUser(ctx, layerImage{Image: empty.Image, layers: imageLayers}, specification)
}

// ResolveUser resolves a Docker/OCI user expression with the final
// /etc/passwd and /etc/group visible after all image layers are applied.
func ResolveUser(ctx context.Context, image v1.Image, specification string) (string, error) {
	if image == nil || specification == "" {
		return "", ErrUnknownUser
	}
	files, err := accountFiles(ctx, image)
	if err != nil {
		return "", err
	}
	var passwd io.Reader
	if content, found := files[passwdPath]; found {
		passwd = bytes.NewReader(content)
	}
	var groups io.Reader
	if group, present := files[groupPath]; present {
		groups = bytes.NewReader(group)
	}
	resolved, err := userdb.GetExecUser(
		specification,
		&userdb.ExecUser{Uid: 0, Gid: 0, Sgids: []int{}, Home: ""},
		passwd,
		groups,
	)
	if err != nil || resolved == nil || resolved.Uid < 0 || resolved.Gid < 0 {
		return "", ErrUnknownUser
	}

	return strconv.Itoa(resolved.Uid) + ":" + strconv.Itoa(resolved.Gid), nil
}

func accountFiles(ctx context.Context, image v1.Image) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read image account database: %w", err)
	}
	rootfs := mutate.Extract(image)
	defer func() {
		_ = rootfs.Close()
	}()

	return readAccountFiles(ctx, rootfs, maximumRootFSEntries)
}

func readAccountFiles(ctx context.Context, rootfs io.Reader, maximumEntries int) (map[string][]byte, error) {
	files := make(map[string][]byte, accountFileCount)
	reader := tar.NewReader(rootfs)
	for entries := 0; ; entries++ {
		if entries == maximumEntries {
			return nil, ErrInvalidImage
		}
		header, done, err := nextAccountFile(ctx, reader)
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
		if header == nil {
			continue
		}
		value, err := readAccountFile(reader, header.Size)
		if err != nil {
			return nil, ErrInvalidImage
		}
		files[header.Name] = value
	}

	return files, nil
}

func nextAccountFile(ctx context.Context, reader *tar.Reader) (*tar.Header, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("read image account database: %w", err)
	}
	header, err := reader.Next()
	if errors.Is(err, io.EOF) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, ErrInvalidImage
	}
	if header.Name != passwdPath && header.Name != groupPath {
		return nil, false, nil
	}
	if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maximumAccountFileSize {
		return nil, false, ErrInvalidImage
	}

	return header, false, nil
}

func readAccountFile(reader io.Reader, expectedSize int64) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(reader, maximumAccountFileSize+1))
	if err != nil || int64(len(value)) != expectedSize {
		return nil, ErrInvalidImage
	}

	return value, nil
}

type compressedLayer struct {
	digest    v1.Hash
	size      int64
	mediaType types.MediaType
	open      func() (io.ReadCloser, error)
}

//nolint:ireturn // v1.Layer is the extension contract required by go-containerregistry.
func compressedLayerValue(value CompressedLayer) (v1.Layer, error) {
	digest, err := v1.NewHash(value.Digest)
	mediaType := types.MediaType(value.MediaType)
	if err != nil || value.Size <= 0 || value.Open == nil || !supportedLayerMediaType(mediaType) {
		return nil, ErrInvalidImage
	}

	return partial.CompressedToLayer(&compressedLayer{ //nolint:wrapcheck // Validated layer methods cannot return errors.
		digest: digest, size: value.Size, mediaType: mediaType, open: value.Open,
	})
}

func supportedLayerMediaType(value types.MediaType) bool {
	return slices.Contains([]types.MediaType{
		types.DockerLayer,
		types.DockerUncompressedLayer,
		types.DockerForeignLayer,
		types.OCILayer,
		types.OCIUncompressedLayer,
		types.OCILayerZStd,
		types.OCIRestrictedLayer,
		types.OCIUncompressedRestrictedLayer,
	}, value)
}

func (layer *compressedLayer) Digest() (v1.Hash, error) {
	return layer.digest, nil
}

func (layer *compressedLayer) Compressed() (io.ReadCloser, error) {
	return layer.open()
}

func (layer *compressedLayer) Size() (int64, error) {
	return layer.size, nil
}

func (layer *compressedLayer) MediaType() (types.MediaType, error) {
	return layer.mediaType, nil
}

type layerImage struct {
	v1.Image

	layers []v1.Layer
}

func (image layerImage) Layers() ([]v1.Layer, error) {
	return slices.Clone(image.layers), nil
}
