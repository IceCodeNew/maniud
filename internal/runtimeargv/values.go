package runtimeargv

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	maximumSignedInteger = math.MaxInt64
	maximumMemoryBytes   = math.MaxInt64 / 2
	maximumOOMScore      = 1000
	minimumBlkioWeight   = 10
	maximumBlkioWeight   = 1000
	maximumGroupID       = math.MaxUint32 - 1
	maximumHealthRetries = math.MaxInt32
	maximumTextLength    = maximumArgumentLength
	maximumTmpfsOptions  = 8
	maximumSysctls       = 128
	bytesPerKibibyte     = 1024
)

var (
	cgroupParentPattern = regexp.MustCompile(`^[/A-Za-z0-9_.:@-]+$`)
	capabilityPattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	hostnamePattern     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]*[A-Za-z0-9])?$`)
	restartPattern      = regexp.MustCompile(`^(?:no|always|unless-stopped|on-failure(?::[1-9][0-9]*)?)$`)
	sysctlKeyPattern    = regexp.MustCompile(`^[a-z0-9_]+(?:\.[a-z0-9_]+)+$`)
	environmentPattern  = regexp.MustCompile(`^[^\s=\x00-\x1f\x7f]+$`)
	ulimitNamePattern   = regexp.MustCompile(
		`^(?:nofile|nproc|core|cpu|data|fsize|locks|memlock|msgqueue|nice|rss|rtprio|rttime|sigpending|stack)$`,
	)
)

func validCgroupParent(value string) bool {
	return len(value) <= 255 && cgroupParentPattern.MatchString(value)
}

func canonicalBoundedInteger(value string, minimum, maximum int64, sign bool) (string, error) {
	if value == "" || (!sign && strings.HasPrefix(value, "+")) {
		return "", ErrInvalid
	}
	digits := strings.TrimLeft(value, "+-")
	if !asciiDigits(digits) {
		return "", ErrInvalid
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		return "", ErrInvalid
	}

	return strconv.FormatInt(parsed, 10), nil
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}

	return true
}

func canonicalByteSize(value string, maximum int64) (string, error) {
	factor := int64(1)
	digits := value
	if len(value) > 1 {
		switch value[len(value)-1] {
		case 'b':
			digits = value[:len(value)-1]
		case 'k':
			factor = bytesPerKibibyte
			digits = value[:len(value)-1]
		case 'm':
			factor = bytesPerKibibyte * bytesPerKibibyte
			digits = value[:len(value)-1]
		case 'g':
			factor = bytesPerKibibyte * bytesPerKibibyte * bytesPerKibibyte
			digits = value[:len(value)-1]
		}
	}
	if !asciiDigits(digits) || digits[0] == '0' {
		return "", ErrInvalid
	}
	parsed, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || parsed > maximum/factor {
		return "", ErrInvalid
	}

	return strconv.FormatInt(parsed*factor, 10), nil
}

//nolint:cyclop // Decimal CPU syntax is canonicalized without a binary float.
func canonicalCPU(value string) (string, error) {
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return "", ErrInvalid
	}
	integer, fraction, hasFraction := strings.Cut(value, ".")
	if !hasFraction {
		if !asciiDigits(integer) {
			return "", ErrInvalid
		}
	} else if (integer != "" && !asciiDigits(integer)) ||
		(fraction != "" && !asciiDigits(fraction)) || len(fraction) > 5 ||
		(integer == "" && fraction == "") {
		return "", ErrInvalid
	}
	integer = strings.TrimLeft(integer, "0")
	if integer == "" {
		integer = "0"
	}
	fraction = strings.TrimRight(fraction, "0")
	canonical := integer
	if fraction != "" {
		canonical += "." + fraction
	}
	if canonical == "0" {
		return "", ErrInvalid
	}

	return canonical, nil
}

func composeCPU(canonical string) (string, bool, error) {
	parsed, err := strconv.ParseFloat(canonical, 32)
	if err != nil {
		return "", false, ErrInvalid
	}
	effective := strconv.FormatFloat(parsed, 'f', -1, 32)

	return effective, effective != canonical, nil
}

func canonicalCapability(value string) (string, error) {
	canonical := strings.ToUpper(strings.TrimPrefix(strings.ToUpper(value), "CAP_"))
	if len(canonical) == 0 || len(canonical) > 64 || !capabilityPattern.MatchString(canonical) {
		return "", ErrInvalid
	}

	return canonical, nil
}

func validDomain(value string) bool {
	return validOptionText(value)
}

func validHostname(value string) bool {
	return len(value) <= 253 && hostnamePattern.MatchString(value)
}

func validOptionText(value string) bool {
	return len(value) <= maximumTextLength && validText(value) && !strings.ContainsAny(value, " \t\r\n")
}

func validUlimitName(value string) bool {
	return ulimitNamePattern.MatchString(value)
}

func validRestartPolicy(value string) bool {
	return restartPattern.MatchString(value)
}

func validEnvironmentName(value string) bool {
	return len(value) <= 255 && environmentPattern.MatchString(value)
}

func validSysctlKey(value string) bool {
	return len(value) <= 255 && sysctlKeyPattern.MatchString(value)
}

func validSysctlValue(value string) bool {
	if !validText(value) {
		return false
	}
	for _, character := range value {
		if character > 127 || character < 32 || character == 127 {
			return false
		}
	}

	return true
}

func namespacedSysctl(value string) bool {
	switch value {
	case "kernel.msgmax", "kernel.msgmnb", "kernel.msgmni", "kernel.sem", "kernel.shmall",
		"kernel.shmmax", "kernel.shmmni", "kernel.shm_rmid_forced":
		return true
	default:
		return strings.HasPrefix(value, "fs.mqueue.") || strings.HasPrefix(value, "net.")
	}
}
