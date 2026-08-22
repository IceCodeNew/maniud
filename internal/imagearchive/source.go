package imagearchive

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/imageref"
)

const (
	sourcePrefix       = "docker-archive:"
	maximumSourceBytes = 32 << 10
)

// Source is one absolute Docker archive path and exact member selector.
type Source struct {
	path         string
	selector     string
	strictSingle bool
}

// ParseSource parses docker-archive:PATH:TAG and docker-archive:PATH@INDEX.
func ParseSource(value string) (Source, error) {
	var empty Source
	if len(value) <= len(sourcePrefix) || len(value) > maximumSourceBytes || !strings.HasPrefix(value, sourcePrefix) ||
		!utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || strings.ContainsAny(value, "\r\n") {
		return empty, ErrInvalidSource
	}

	raw := strings.TrimPrefix(value, sourcePrefix)
	if source, valid := indexedSource(raw); valid {
		return source, nil
	}

	candidates := taggedSources(raw)
	if len(candidates) != 1 {
		candidates = existingSources(candidates)
	}
	if len(candidates) != 1 {
		return empty, ErrInvalidSource
	}

	return candidates[0], nil
}

func indexedSource(value string) (Source, bool) {
	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 || separator == len(value)-1 {
		return Source{}, false
	}
	pathValue, indexValue := value[:separator], value[separator+1:]
	if strings.Trim(indexValue, "0123456789") != "" {
		return Source{}, false
	}

	pathValue = strings.TrimSuffix(pathValue, ":")
	index, err := strconv.ParseUint(indexValue, 10, 32)
	if err != nil || index >= maximumArchiveMembers || !validAbsolutePath(pathValue) {
		return Source{}, false
	}

	return Source{path: filepath.Clean(pathValue), selector: "@" + strconv.FormatUint(index, 10)}, true
}

func taggedSources(value string) []Source {
	result := make([]Source, 0, 1)
	for index := range len(value) {
		if value[index] != ':' {
			continue
		}

		pathValue, selector := value[:index], value[index+1:]
		if !validAbsolutePath(pathValue) || !explicitTaggedSelector(selector) {
			continue
		}

		normalized, err := imageref.Normalize(selector)
		if err != nil {
			continue
		}

		result = append(result, Source{path: filepath.Clean(pathValue), selector: normalized.String()})
	}

	return result
}

func existingSources(values []Source) []Source {
	result := make([]Source, 0, 1)
	for _, value := range values {
		if _, err := os.Lstat(value.path); err == nil {
			result = append(result, value)
		}
	}

	return result
}

func explicitTaggedSelector(value string) bool {
	return value != "" && !strings.ContainsRune(value, '@') &&
		strings.LastIndexByte(value, ':') > strings.LastIndexByte(value, '/')
}

func validAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value &&
		strings.IndexByte(value, 0) < 0
}

// Path returns the absolute operator-owned archive path.
func (source Source) Path() string {
	return source.path
}

// Selector returns the canonical tagged reference or source index.
func (source Source) Selector() string {
	return source.selector
}

func validSelector(value string) bool {
	if index, found := strings.CutPrefix(value, "@"); found {
		_, err := strconv.ParseUint(index, 10, 32)

		return err == nil
	}

	return explicitTaggedSelector(value)
}
