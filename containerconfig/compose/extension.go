package compose

import (
	"math"
	"strings"
)

const maximumExtensionDepth = 64

func normalizeExtensions(values map[string]any) (map[string]any, bool) {
	if values == nil {
		return nil, true
	}
	normalized := make(map[string]any, len(values))
	for name, value := range values {
		if len(name) <= 2 || !strings.HasPrefix(strings.ToLower(name), "x-") {
			return nil, false
		}
		converted, ok := normalizeExtensionValue(value, 0)
		if !ok {
			return nil, false
		}
		normalized[name] = converted
	}

	return normalized, true
}

//nolint:cyclop // The accepted extension data tree is an explicit closed type union.
func normalizeExtensionValue(value any, depth int) (any, bool) {
	if depth > maximumExtensionDepth {
		return nil, false
	}
	switch typed := value.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return value, true
	case float32:
		number := float64(typed)

		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			converted, ok := normalizeExtensionValue(item, depth+1)
			if !ok {
				return nil, false
			}
			result[index] = converted
		}

		return result, true
	case map[string]any:
		result := make(map[string]any, len(typed))
		for name, item := range typed {
			converted, ok := normalizeExtensionValue(item, depth+1)
			if !ok {
				return nil, false
			}
			result[name] = converted
		}

		return result, true
	default:
		return nil, false
	}
}
