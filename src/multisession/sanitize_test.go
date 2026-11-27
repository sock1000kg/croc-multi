package multisession

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "an-pc", "an-pc"},
		{"vietnamese kept", "Nguyễn", "Nguyễn"},
		{"space kept", "an pc", "an pc"},
		{"ESC removed", "\x1b[31mevil", "[31mevil"},
		{"C1 CSI removed", "a\u009b31mb", "a31mb"},
		{"bidi override removed", "abc‮txt.exe", "abctxt.exe"},
		{"zero width removed", "a​b", "ab"},
		{"newline and tab removed", "a\nb\tc\r", "abc"},
		{"NUL and DEL removed", "a\x00b\x7f", "ab"},
		{"empty", "", "?"},
		{"only controls", "\x1b\x07\n", "?"},
		{"cut to 32", strings.Repeat("x", 40), strings.Repeat("x", 32)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeName(tt.in); got != tt.want {
				t.Fatalf("sanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Cutting counts runes, so a multi-byte name is never split mid-character.
func TestSanitizeNameCutsRunes(t *testing.T) {
	got := sanitizeName(strings.Repeat("ễ", 40))
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 32 {
		t.Fatalf("got %d runes (valid=%v), want 32 valid runes", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
}
