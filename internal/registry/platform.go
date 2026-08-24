package registry

import (
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func normalizePlatform(value domain.Platform) (imagePlatform, error) {
	if value.OS == "" || value.Architecture == "" ||
		strings.ContainsAny(value.OS+value.Architecture+value.Variant, ", ") ||
		strings.ToLower(value.OS) != value.OS || strings.ToLower(value.Architecture) != value.Architecture ||
		strings.ToLower(value.Variant) != value.Variant {
		return imagePlatform{}, ErrInvalidSource
	}

	return imagePlatform{
		OS:           value.OS,
		Architecture: value.Architecture,
		Variant:      value.Variant,
		OSVersion:    "",
		OSFeatures:   nil,
		Features:     nil,
	}, nil
}
