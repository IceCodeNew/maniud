package compose

import (
	"strings"

	orasregistry "oras.land/oras-go/v2/registry"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func pinnedImageDigest(value string) (domain.Digest, bool) {
	repositorySeparator := strings.IndexByte(value, '/')

	digestSeparator := strings.LastIndexByte(value, '@')
	if repositorySeparator <= 0 || digestSeparator <= repositorySeparator ||
		value[:repositorySeparator] != strings.ToLower(value[:repositorySeparator]) {
		return domain.Digest{}, false
	}

	digest, err := domain.ParseDigest(value[digestSeparator+1:])
	if err != nil {
		return domain.Digest{}, false
	}

	nameAndTag := value[:digestSeparator]
	tagSeparator := strings.LastIndexByte(nameAndTag, ':')
	repositoryEnd := len(nameAndTag)
	tag := ""

	if tagSeparator > repositorySeparator {
		repositoryEnd = tagSeparator
		tag = nameAndTag[tagSeparator+1:]

		if tag == "" {
			return domain.Digest{}, false
		}
	}

	reference := orasregistry.Reference{
		Registry:   value[:repositorySeparator],
		Repository: value[repositorySeparator+1 : repositoryEnd],
		Reference:  digest.String(),
	}
	if reference.Validate() != nil {
		return domain.Digest{}, false
	}

	if tag != "" {
		reference.Reference = tag
		if reference.ValidateReferenceAsTag() != nil {
			return domain.Digest{}, false
		}
	}

	return digest, true
}
