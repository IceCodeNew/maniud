package runtimeargv

import (
	"regexp"
	"strconv"
)

var durationPattern = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h))+$`)

func (parser *argvParser) setHealth(field, value string) error {
	canonical, err := canonicalHealthValue(field, value)
	if err != nil {
		return err
	}
	key := "health_" + field
	if previous, found := parser.seenScalars[key]; found {
		if previous != canonical {
			return ErrInvalid
		}

		return nil
	}
	parser.seenScalars[key] = canonical
	parser.healthFields = true
	switch field {
	case healthCommand:
		parser.health.Test = []string{"CMD-SHELL", canonical}
	case healthInterval:
		parser.health.Interval = canonical
	case healthRetries:
		parser.setHealthRetries(canonical)
	case healthStartInterval:
		parser.health.StartInterval = canonical
	case healthStartPeriod:
		parser.health.StartPeriod = canonical
	default:
		// canonicalHealthValue leaves timeout as the only remaining field.
		parser.health.Timeout = canonical
	}

	return nil
}

func (parser *argvParser) setHealthRetries(value string) {
	parsed, _ := strconv.ParseInt(value, 10, 32)
	if parsed != 0 {
		retries := int(parsed)
		parser.health.Retries = &retries
	}
}

func canonicalHealthValue(field, value string) (string, error) {
	switch field {
	case healthCommand:
		if validText(value) {
			return value, nil
		}
	case healthRetries:
		return canonicalBoundedInteger(value, 0, maximumHealthRetries, false)
	case healthInterval, healthStartInterval, healthStartPeriod, healthTimeout:
		if len(value) <= maximumTextLength && durationPattern.MatchString(value) {
			return value, nil
		}
	}

	return "", ErrInvalid
}
