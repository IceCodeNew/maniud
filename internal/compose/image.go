package compose

import (
	"strings"

	"github.com/regclient/regclient/types/ref"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func pinnedImageDigest(value string) (domain.Digest, bool) {
	parsed, err := ref.New(value)
	if err != nil || parsed.CommonName() != value {
		return domain.Digest{}, false
	}

	slash := strings.IndexByte(value, '/')

	at := strings.LastIndexByte(value, '@')
	if slash <= 0 || at <= slash || value[:slash] != strings.ToLower(value[:slash]) {
		return domain.Digest{}, false
	}

	digest, err := domain.ParseDigest(value[at+1:])

	return digest, err == nil
}
