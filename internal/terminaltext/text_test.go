package terminaltext

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func broadLimits() Limits {
	return Limits{Bytes: 1024, Runes: 1024, Lines: 8, LineCells: 512}
}

func TestCanonicalizePreservesTextAndEscapesUnsafeCharacters(t *testing.T) {
	t.Parallel()

	unsafe := string([]rune{
		0x00, 0x09, 0x0d, 0x1b, 0x7f, 0x85, 0x061c, 0x200e, 0x200f,
		0x2028, 0x2029, 0x202a, 0x202e, 0x2066, 0x2069, 0xfdd0, 0xfdef, 0xfffe, 0x1fffe, 0x10ffff,
	})
	wide := string([]rune{0x4e16, 0x754c})
	value := "service " + wide + "\ne\u0301" + unsafe
	got, err := Canonicalize(value, broadLimits())
	if err != nil {
		t.Fatalf("Canonicalize() error = %v", err)
	}
	wantSuffix := strings.Join([]string{
		`\u0000`, `\u0009`, `\u000D`, `\u001B`, `\u007F`, `\u0085`, `\u061C`, `\u200E`, `\u200F`,
		`\u2028`, `\u2029`, `\u202A`, `\u202E`, `\u2066`, `\u2069`, `\uFDD0`, `\uFDEF`, `\uFFFE`,
		`\U0001FFFE`, `\U0010FFFF`,
	}, "")
	if got != "service "+wide+"\ne\u0301"+wantSuffix {
		t.Fatalf("Canonicalize() = %q", got)
	}
}

func TestCanonicalizeRejectsInvalidInputAndBounds(t *testing.T) {
	t.Parallel()

	invalidUTF8 := string([]byte{0xff})
	if _, err := Canonicalize(invalidUTF8, broadLimits()); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("Canonicalize(invalid UTF-8) error = %v", err)
	}

	invalidLimits := []Limits{
		{Bytes: 0, Runes: 1, Lines: 1, LineCells: 1},
		{Bytes: 1, Runes: 0, Lines: 1, LineCells: 1},
		{Bytes: 1, Runes: 1, Lines: 0, LineCells: 1},
		{Bytes: 1, Runes: 1, Lines: 1, LineCells: 0},
	}
	for _, limits := range invalidLimits {
		if _, err := Canonicalize("", limits); !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("Canonicalize(invalid limits %#v) error = %v", limits, err)
		}
	}
}

func TestCanonicalizeEnforcesEveryLimit(t *testing.T) {
	t.Parallel()
	wide := string([]rune{0x4e16, 0x754c})

	tests := []struct {
		name   string
		value  string
		limits Limits
	}{
		{name: "input bytes", value: "ab", limits: Limits{Bytes: 1, Runes: 8, Lines: 1, LineCells: 8}},
		{name: "escaped bytes", value: "\x00", limits: Limits{Bytes: 5, Runes: 8, Lines: 1, LineCells: 8}},
		{name: "runes", value: "ab", limits: Limits{Bytes: 8, Runes: 1, Lines: 1, LineCells: 8}},
		{name: "lines", value: "a\nb", limits: Limits{Bytes: 8, Runes: 8, Lines: 1, LineCells: 8}},
		{name: "line cells", value: wide, limits: Limits{Bytes: 8, Runes: 8, Lines: 1, LineCells: 3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Canonicalize(test.value, test.limits); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("Canonicalize(%q) error = %v", test.value, err)
			}
		})
	}
}

func TestCanonicalizePreservesSafeBoundaryCharacters(t *testing.T) {
	t.Parallel()

	value := string([]rune{
		0x20, 0x7e, 0xa0, 0x061b, 0x200d, 0x2027, 0x202f, 0x2065, 0x206a,
		0xfdcf, 0xfdf0, 0xfffd, 0x10000, 0x1fffd, 0x10fffd,
	})
	got, err := Canonicalize(value, broadLimits())
	if err != nil || got != value {
		t.Fatalf("Canonicalize(safe boundaries) = %q, %v", got, err)
	}
}

func TestCellAwareFitting(t *testing.T) {
	t.Parallel()

	value := "ab" + string([]rune{0x4e16, 0x754c}) + "e\u0301cd"
	if got := Width(value); got != 9 {
		t.Fatalf("Width() = %d, want 9", got)
	}
	if got := Clip(value, 0); got != "" {
		t.Fatalf("Clip(0) = %q", got)
	}
	if got := Clip(value, 6); got != "ab"+string([]rune{0x4e16, 0x754c}) {
		t.Fatalf("Clip(6) = %q", got)
	}
	if got := Middle(value, 9, "…"); got != value {
		t.Fatalf("Middle(fits) = %q", got)
	}
	if got := Middle(value, 1, "..."); got != "." {
		t.Fatalf("Middle(marker only) = %q", got)
	}
	if got := Middle(value, 8, "…"); got != "ab"+string(rune(0x4e16))+"…e\u0301cd" || Width(got) != 8 {
		t.Fatalf("Middle(8) = %q, width %d", got, Width(got))
	}
}

func TestWrapPreservesGraphemesAndNewlines(t *testing.T) {
	t.Parallel()

	if got := Wrap("value", 0); len(got) != 0 || got == nil {
		t.Fatalf("Wrap(0) = %q", got)
	}
	wide := string([]rune{0x4e16, 0x754c})
	got := Wrap("ab"+wide+"\ne\u0301fg", 4)
	want := []string{"ab" + string(rune(0x4e16)), string(rune(0x754c)), "e\u0301fg"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Wrap() = %q, want %q", got, want)
	}
}

func FuzzCanonicalize(f *testing.F) {
	f.Add("plain")
	f.Add("line\n\x1b[31m")
	f.Add(string([]byte{0xff}))

	f.Fuzz(func(t *testing.T, value string) {
		limits := Limits{Bytes: 256, Runes: 256, Lines: 8, LineCells: 80}
		canonical, err := Canonicalize(value, limits)
		if err != nil {
			return
		}
		if !utf8.ValidString(canonical) || len(canonical) > limits.Bytes ||
			utf8.RuneCountInString(canonical) > limits.Runes {
			t.Fatalf("Canonicalize() returned invalid bounded text %q", canonical)
		}
		for _, character := range canonical {
			if unsafeCharacter(character) {
				t.Fatalf("Canonicalize() retained unsafe character U+%04X", character)
			}
		}
	})
}
