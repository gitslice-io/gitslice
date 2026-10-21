package socialcard

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderDrawsACardOfTheSharedSize(t *testing.T) {
	cards := map[string]Card{
		"slice": {
			Account:     "sunny",
			Slice:       "ryujinx-neo",
			Description: "Mirror of Ryujinx_Neo by Sunshinin11 (https://github.com/Sunshinin11/Ryujinx_Neo), MIT license",
			Facts:       []string{"Public slice", "gitslice.io/slices/sunny/ryujinx-neo"},
		},
		"long": {
			Account:     "an-organization-with-a-long-name",
			Slice:       "a-slice-whose-name-is-far-too-long-to-fit-on-one-line-of-the-card",
			Description: strings.Repeat("A description that goes on and on, with ümlauts, 日本語 and emoji 🚀. ", 12),
			Facts:       []string{"Public slice"},
		},
		"generic": {},
	}
	for name, card := range cards {
		data, err := Render(card)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := img.Bounds(); b.Dx() != Width || b.Dy() != Height {
			t.Fatalf("%s: size %v, want %dx%d", name, b, Width, Height)
		}
		if dir := os.Getenv("SOCIALCARD_OUT"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, name+".png"), data, 0o644)
		}
	}
}

func TestWrapKeepsToTheLineLimitWithAnEllipsis(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	body, err := face(f.regular, 34)
	if err != nil {
		t.Fatal(err)
	}
	lines := wrap(body, strings.Repeat("word ", 200), 600, 3)
	if len(lines) != 3 {
		t.Fatalf("wrap gave %d lines, want 3", len(lines))
	}
	if !strings.HasSuffix(lines[2], "…") {
		t.Fatalf("last line %q does not end with an ellipsis", lines[2])
	}
	for _, line := range lines {
		if w := measure(body, line); w > 600 {
			t.Fatalf("line %q is %d wide, want at most 600", line, w)
		}
	}
	if got := wrap(body, "short", 600, 3); len(got) != 1 || got[0] != "short" {
		t.Fatalf("wrap(short) = %q", got)
	}
	long := wrap(body, strings.Repeat("x", 300), 600, 2)
	if len(long) != 2 || !strings.HasSuffix(long[1], "…") {
		t.Fatalf("an unbreakable word wraps to %q", long)
	}
}
