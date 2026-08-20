package registry

import (
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func readVerified(reader io.ReadCloser, descriptorValue ocispec.Descriptor, maximum int64) ([]byte, error) {
	if reader == nil {
		return nil, ErrProtocol
	}

	if descriptorValue.Size <= 0 || descriptorValue.Size > maximum {
		_ = reader.Close()

		return nil, ErrProtocol
	}

	raw, readErr := io.ReadAll(io.LimitReader(reader, maximum+1))
	closeErr := reader.Close()

	if readErr != nil {
		return nil, fmt.Errorf("read registry content: %w", readErr)
	}

	if closeErr != nil {
		return nil, fmt.Errorf("close registry content: %w", closeErr)
	}

	if _, valid := validRawDescriptor(descriptorValue, raw, maximum); !valid {
		return nil, ErrProtocol
	}

	return raw, nil
}
