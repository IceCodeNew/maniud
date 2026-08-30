// Package terminaltext owns canonical terminal-safe text and cell-aware fitting.
package terminaltext

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const (
	maximumBMPCodePoint = 0xffff
	prefixShare         = 2
	totalShares         = 3
)

var (
	// ErrInvalidLimits reports a missing or negative canonical-text bound.
	ErrInvalidLimits = errors.New("terminal text limits are invalid")
	// ErrInvalidUTF8 reports input that is not valid UTF-8.
	ErrInvalidUTF8 = errors.New("terminal text is not valid UTF-8")
	// ErrLimitExceeded reports canonical text outside a caller-owned bound.
	ErrLimitExceeded = errors.New("terminal text exceeds its limit")
)

// Limits bounds the canonical representation. Every field must be positive.
type Limits struct {
	Bytes     int
	Runes     int
	Lines     int
	LineCells int
}

// Canonicalize converts terminal controls and direction-changing code points
// into visible escapes. The returned string is the only representation callers
// should retain, validate, display, export, or send again.
func Canonicalize(value string, limits Limits) (string, error) {
	if limits.Bytes <= 0 || limits.Runes <= 0 || limits.Lines <= 0 || limits.LineCells <= 0 {
		return "", ErrInvalidLimits
	}
	if !utf8.ValidString(value) {
		return "", ErrInvalidUTF8
	}
	if len(value) > limits.Bytes {
		return "", ErrLimitExceeded
	}

	var canonical strings.Builder
	canonical.Grow(len(value))
	for _, character := range value {
		if unsafeCharacter(character) {
			canonical.WriteString(visibleEscape(character))

			continue
		}
		canonical.WriteRune(character)
	}
	result := canonical.String()
	if !withinLimits(result, limits) {
		return "", ErrLimitExceeded
	}

	return result, nil
}

func unsafeCharacter(character rune) bool {
	return character != '\n' && (character <= 0x1f || character >= 0x7f && character <= 0x9f) ||
		character == 0x2028 || character == 0x2029 || bidirectionalControl(character) ||
		noncharacter(character)
}

func bidirectionalControl(character rune) bool {
	return character == 0x061c || character == 0x200e || character == 0x200f ||
		character >= 0x202a && character <= 0x202e || character >= 0x2066 && character <= 0x2069
}

func noncharacter(character rune) bool {
	return character >= 0xfdd0 && character <= 0xfdef || character&0xffff == 0xfffe ||
		character&0xffff == 0xffff
}

func visibleEscape(character rune) string {
	if character <= maximumBMPCodePoint {
		return fmt.Sprintf("\\u%04X", character)
	}

	return fmt.Sprintf("\\U%08X", character)
}

func withinLimits(value string, limits Limits) bool {
	if len(value) > limits.Bytes || utf8.RuneCountInString(value) > limits.Runes {
		return false
	}

	lines := 0
	for line := range strings.SplitSeq(value, "\n") {
		lines++
		if lines > limits.Lines || ansi.StringWidth(line) > limits.LineCells {
			return false
		}
	}

	return true
}

// Width reports the terminal-cell width of one canonical string.
func Width(value string) int {
	return ansi.StringWidth(value)
}

// Clip keeps the leading grapheme clusters that fit in cells.
func Clip(value string, cells int) string {
	if cells <= 0 {
		return ""
	}

	return ansi.Truncate(value, cells, "")
}

// Middle keeps a two-thirds prefix and one-third suffix around marker. It
// treats value and marker as canonical single-line text.
func Middle(value string, cells int, marker string) string {
	width := Width(value)
	if width <= cells {
		return value
	}
	markerWidth := Width(marker)
	if cells <= markerWidth {
		return Clip(marker, cells)
	}

	remaining := cells - markerWidth
	prefixWidth := remaining * prefixShare / totalShares
	suffixWidth := remaining - prefixWidth
	prefix := ansi.Cut(value, 0, prefixWidth)
	suffix := ansi.Cut(value, width-suffixWidth, width)

	return prefix + marker + suffix
}

// Wrap breaks canonical text at terminal-cell boundaries without splitting
// grapheme clusters. Existing newlines remain line boundaries.
func Wrap(value string, cells int) []string {
	if cells <= 0 {
		return nil
	}

	return strings.Split(ansi.Hardwrap(value, cells, false), "\n")
}
