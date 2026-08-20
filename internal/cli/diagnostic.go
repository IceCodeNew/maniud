package cli

import (
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maximumDiagnosticCauses       = 32
	maximumDiagnosticCallSites    = 16
	maximumDiagnosticCauseBytes   = 12 << 10
	maximumDiagnosticSiteBytes    = 4 << 10
	maximumDiagnosticMessageSize  = 4 << 10
	maximumDiagnosticTypeSize     = 512
	maximumDiagnosticSiteSize     = 1 << 10
	diagnosticCallSitePCsPerFrame = 4
	diagnosticRuntimeCallersSkip  = 2
	diagnosticRedaction           = "[REDACTED]"
	diagnosticEllipsis            = "…"
	projectFunctionPrefix         = "github.com/IceCodeNew/maniud/"
)

type publicDiagnostic struct {
	Causes    []publicDiagnosticCause `json:"causes"`
	CallSites []string                `json:"call_sites"`
	Truncated bool                    `json:"truncated,omitempty"`
}

type publicDiagnosticCause struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type internalDiagnosticError string

func (diagnostic internalDiagnosticError) Error() string {
	return string(diagnostic)
}

type diagnosticPattern struct {
	pattern     *regexp.Regexp
	replacement string
}

type diagnosticRedactor struct {
	patterns          []diagnosticPattern
	environmentValues []string
}

func buildPublicDiagnostic(cause error, environment map[string]string) *publicDiagnostic {
	redactor := newDiagnosticRedactor(environment)
	causes, causeTruncated := diagnosticCauses(cause, redactor)
	callSites, sitesTruncated := diagnosticCallSites(redactor)

	return &publicDiagnostic{
		Causes: causes, CallSites: callSites, Truncated: causeTruncated || sitesTruncated,
	}
}

func newDiagnosticRedactor(environment map[string]string) diagnosticRedactor {
	values := make([]string, 0, len(environment))
	for _, value := range environment {
		if value != "" {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(left, right int) bool {
		return len(values[left]) > len(values[right])
	})

	return diagnosticRedactor{
		patterns: []diagnosticPattern{
			{
				pattern: regexp.MustCompile(
					`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`,
				),
				replacement: diagnosticRedaction,
			},
			{
				pattern: regexp.MustCompile(
					`(?im)(authorization|proxy-authorization|x-registry-auth)[ \t]*[:=][ \t]*[^\r\n]*`,
				),
				replacement: `${1}: ` + diagnosticRedaction,
			},
			{
				pattern: regexp.MustCompile(
					`(?i)\b(password|passwd|secret|token|api[_-]?key|access[_-]?key|` +
						`private[_-]?key|client[_-]?secret)\b[ \t]*[:=][ \t]*` +
						`(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`,
				),
				replacement: `${1}=` + diagnosticRedaction,
			},
			{
				pattern:     regexp.MustCompile(`(?i)\b(bearer|basic)[ \t]+[A-Za-z0-9._~+/=-]+`),
				replacement: `${1} ` + diagnosticRedaction,
			},
			{
				pattern:     regexp.MustCompile(`(?i)(https?://)[^/\s@]+@`),
				replacement: `${1}` + diagnosticRedaction + `@`,
			},
		},
		environmentValues: values,
	}
}

func (redactor diagnosticRedactor) redact(value string) string {
	redacted := strings.ToValidUTF8(value, "�")
	for _, pattern := range redactor.patterns {
		redacted = pattern.pattern.ReplaceAllString(redacted, pattern.replacement)
	}
	for _, environmentValue := range redactor.environmentValues {
		redacted = strings.ReplaceAll(redacted, environmentValue, diagnosticRedaction)
	}

	return redacted
}

func diagnosticCauses(cause error, redactor diagnosticRedactor) ([]publicDiagnosticCause, bool) {
	causes := make([]publicDiagnosticCause, 0)
	if cause == nil {
		return causes, false
	}

	remaining := maximumDiagnosticCauseBytes
	truncated := false
	stack := []error{cause}
	for len(stack) > 0 {
		if len(causes) == maximumDiagnosticCauses || remaining == 0 {
			truncated = true

			break
		}

		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current == nil {
			continue
		}

		typeName, used, typeTruncated := diagnosticValue(
			fmt.Sprintf("%T", current), redactor, min(maximumDiagnosticTypeSize, remaining),
		)
		remaining -= used
		message, used, messageTruncated := diagnosticValue(
			current.Error(), redactor, min(maximumDiagnosticMessageSize, remaining),
		)
		remaining -= used
		truncated = truncated || typeTruncated || messageTruncated
		causes = append(causes, publicDiagnosticCause{Type: typeName, Message: message})

		stack = appendDiagnosticChildren(stack, current)
	}

	return causes, truncated
}

func appendDiagnosticChildren(stack []error, current error) []error {
	if joined, ok := current.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		for _, child := range slices.Backward(children) {
			stack = append(stack, child)
		}

		return stack
	}
	if wrapped, ok := current.(interface{ Unwrap() error }); ok {
		stack = append(stack, wrapped.Unwrap())
	}

	return stack
}

func diagnosticCallSites(redactor diagnosticRedactor) ([]string, bool) {
	programCounters := make([]uintptr, maximumDiagnosticCallSites*diagnosticCallSitePCsPerFrame)
	count := runtime.Callers(diagnosticRuntimeCallersSkip, programCounters)
	frames := runtime.CallersFrames(programCounters[:count])
	callSites := make([]string, 0, maximumDiagnosticCallSites)
	remaining := maximumDiagnosticSiteBytes
	truncated := false

	for {
		frame, more := frames.Next()
		if diagnosticFrame(frame.Function) {
			if len(callSites) == maximumDiagnosticCallSites || remaining == 0 {
				truncated = true

				break
			}
			value, used, valueTruncated := diagnosticValue(
				fmt.Sprintf("%s %s:%d", frame.Function, frame.File, frame.Line),
				redactor,
				min(maximumDiagnosticSiteSize, remaining),
			)
			remaining -= used
			truncated = truncated || valueTruncated
			callSites = append(callSites, value)
		}
		if !more {
			break
		}
	}

	return callSites, truncated
}

func diagnosticFrame(function string) bool {
	if !strings.HasPrefix(function, projectFunctionPrefix) {
		return false
	}

	return !strings.HasSuffix(function, ".buildPublicDiagnostic") &&
		!strings.HasSuffix(function, ".diagnosticCallSites") &&
		!strings.HasSuffix(function, ".emitCommandFailure")
}

func diagnosticValue(value string, redactor diagnosticRedactor, limit int) (string, int, bool) {
	redacted := redactor.redact(value)
	if len(redacted) <= limit {
		return redacted, len(redacted), false
	}

	return truncateDiagnostic(redacted, limit), limit, true
}

func truncateDiagnostic(value string, limit int) string {
	if limit <= len(diagnosticEllipsis) {
		return strings.Repeat(".", limit)
	}

	end := limit - len(diagnosticEllipsis)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}

	return value[:end] + diagnosticEllipsis
}
