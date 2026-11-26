package telegram

import (
	"strings"
	"testing"
)

func TestAStagedFileNameFitsTheFilesystemLimit(t *testing.T) {
	cases := []struct {
		name, given, suffix string
	}{
		{"wide runes", strings.Repeat("😀", 100) + ".txt", ".txt"},
		{"long extension", "notes." + strings.Repeat("x", 300), "notes"},
		{"hidden traversal", "../../.env", "_.._.env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := safeFileName(tc.given, "")
			if len(got) > fileNameMaxBytes || !strings.HasSuffix(got, tc.suffix) || strings.Contains(got, "/") {
				t.Fatalf("safeFileName = %q (%d bytes)", got, len(got))
			}
		})
	}
}
