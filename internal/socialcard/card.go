// Package socialcard draws the preview image a slice link shows when it is
// shared (Open Graph / Twitter cards): a 1200x630 PNG with the slice's name,
// description and a few facts, in the spirit of GitHub's repository cards.
package socialcard

import (
	"bytes"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Width and Height are the card size every major site accepts (1.91:1).
const (
	Width  = 1200
	Height = 630
)

const margin = 80

// Card is what a card shows. Account and Slice are required; an empty Slice
// draws the generic Gitslice card.
type Card struct {
	Account     string
	Slice       string
	Description string
	// Facts are short lines for the footer, such as "Public slice".
	Facts []string
}

var (
	ink     = color.RGBA{0x09, 0x09, 0x0b, 0xff} // zinc-950
	muted   = color.RGBA{0x47, 0x55, 0x69, 0xff} // slate-600
	faint   = color.RGBA{0x94, 0xa3, 0xb8, 0xff} // slate-400
	paper   = color.RGBA{0xff, 0xff, 0xff, 0xff}
	accents = []color.RGBA{
		{0x03, 0x69, 0xa1, 0xff}, // sky-700
		{0x04, 0x78, 0x57, 0xff}, // emerald-700
		{0x6d, 0x28, 0xd9, 0xff}, // violet-700
		{0xb4, 0x53, 0x09, 0xff}, // amber-700
		{0xbe, 0x12, 0x3c, 0xff}, // rose-700
		{0x0f, 0x76, 0x6e, 0xff}, // teal-700
	}
)

type fonts struct {
	regular, medium, bold *opentype.Font
}

var loadFonts = sync.OnceValues(func() (fonts, error) {
	regular, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return fonts{}, err
	}
	medium, err := opentype.Parse(gomedium.TTF)
	if err != nil {
		return fonts{}, err
	}
	bold, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return fonts{}, err
	}
	return fonts{regular: regular, medium: medium, bold: bold}, nil
})

// Render draws card as a PNG.
func Render(card Card) ([]byte, error) {
	f, err := loadFonts()
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(img, img.Bounds(), &image.Uniform{paper}, image.Point{}, draw.Src)
	accent := accentFor(card.Account + "/" + card.Slice)
	// A band along the bottom, like GitHub's language bar.
	draw.Draw(img, image.Rect(0, Height-16, Width, Height), &image.Uniform{accent}, image.Point{}, draw.Src)

	if card.Slice == "" {
		return renderGeneric(img, f)
	}

	const badge = 168
	textRight := Width - margin - badge - 48
	drawInitialBadge(img, f, card.Account, accent, image.Pt(Width-margin-badge, margin), badge)

	// Each block's first baseline sits a line height below the previous
	// block's last one.
	owner, err := face(f.regular, 52)
	if err != nil {
		return nil, err
	}
	y := drawLines(img, owner, muted, wrap(owner, card.Account+" /", textRight-margin, 1), margin, margin+48, 66)

	name, err := face(f.bold, 76)
	if err != nil {
		return nil, err
	}
	y = drawLines(img, name, ink, wrap(name, card.Slice, textRight-margin, 2), margin, y+92, 88)

	if desc := strings.TrimSpace(card.Description); desc != "" {
		body, err := face(f.regular, 34)
		if err != nil {
			return nil, err
		}
		drawLines(img, body, muted, wrap(body, desc, Width-2*margin, 3), margin, y+72, 48)
	}

	footer, err := face(f.medium, 28)
	if err != nil {
		return nil, err
	}
	facts := append([]string(nil), card.Facts...)
	drawLines(img, footer, faint, []string{fit(footer, strings.Join(facts, "  ·  "), Width-2*margin-260)}, margin, Height-72, 0)
	mark, err := face(f.bold, 34)
	if err != nil {
		return nil, err
	}
	drawRight(img, mark, ink, "Gitslice", Width-margin, Height-72)
	return encode(img)
}

func renderGeneric(img *image.RGBA, f fonts) ([]byte, error) {
	title, err := face(f.bold, 96)
	if err != nil {
		return nil, err
	}
	drawLines(img, title, ink, []string{"Gitslice"}, margin, 280, 0)
	body, err := face(f.regular, 40)
	if err != nil {
		return nil, err
	}
	drawLines(img, body, muted, []string{"Slices of one repository, for people and agents."}, margin, 360, 0)
	return encode(img)
}

func encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func face(f *opentype.Font, size float64) (font.Face, error) {
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

// accentFor picks a stable color for a slice, so its card always looks the same.
func accentFor(key string) color.RGBA {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return accents[int(h.Sum32()%uint32(len(accents)))]
}

func drawInitialBadge(img *image.RGBA, f fonts, account string, accent color.RGBA, at image.Point, size int) {
	r := size / 2
	cx, cy := at.X+r, at.Y+r
	for y := at.Y; y < at.Y+size; y++ {
		for x := at.X; x < at.X+size; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, accent)
			}
		}
	}
	initial := "?"
	for _, ch := range account {
		initial = strings.ToUpper(string(ch))
		break
	}
	letter, err := face(f.bold, float64(size)*0.5)
	if err != nil {
		return
	}
	width := font.MeasureString(letter, initial).Round()
	metrics := letter.Metrics()
	baseline := cy + (metrics.Ascent.Round()-metrics.Descent.Round())/2
	drawText(img, letter, paper, initial, cx-width/2, baseline)
}

// drawLines draws lines from baseline y, step apart, and returns the baseline
// of the last line.
func drawLines(img *image.RGBA, f font.Face, c color.Color, lines []string, x, y, step int) int {
	for i, line := range lines {
		if i > 0 {
			y += step
		}
		drawText(img, f, c, line, x, y)
	}
	return y
}

func drawRight(img *image.RGBA, f font.Face, c color.Color, text string, right, y int) {
	drawText(img, f, c, text, right-font.MeasureString(f, text).Round(), y)
}

func drawText(img *image.RGBA, f font.Face, c color.Color, text string, x, y int) {
	d := font.Drawer{Dst: img, Src: &image.Uniform{c}, Face: f, Dot: fixed.P(x, y)}
	d.DrawString(printable(f, text))
}

// printable drops characters the font has no glyph for (it covers Latin,
// Greek and Cyrillic), so they do not show as boxes.
func printable(f font.Face, text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if _, ok := f.GlyphAdvance(r); !ok {
			return -1
		}
		return r
	}, text)
}

// wrap breaks text into at most maxLines lines no wider than width, ending
// the last with an ellipsis when text does not fit. A word wider than a line
// is broken by characters.
func wrap(f font.Face, text string, width, maxLines int) []string {
	words := strings.Fields(printable(f, text))
	var lines []string
	line := ""
	for i := 0; i < len(words); i++ {
		candidate := words[i]
		if line != "" {
			candidate = line + " " + words[i]
		}
		if font.MeasureString(f, candidate).Round() <= width {
			line = candidate
			continue
		}
		if line == "" {
			// One word wider than the line: split it where it overflows.
			head, rest := splitToWidth(f, words[i], width)
			lines = append(lines, head)
			words[i] = rest
			i--
		} else {
			lines = append(lines, line)
			line = ""
			i--
		}
		if len(lines) == maxLines {
			lines[maxLines-1] = fit(f, lines[maxLines-1]+" "+strings.Join(words[i+1:], " "), width)
			return lines
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = fit(f, lines[maxLines-1]+"…", width)
	}
	return lines
}

func splitToWidth(f font.Face, word string, width int) (string, string) {
	runes := []rune(word)
	for n := len(runes); n > 1; n-- {
		if font.MeasureString(f, string(runes[:n])).Round() <= width {
			return string(runes[:n]), string(runes[n:])
		}
	}
	return string(runes[:1]), string(runes[1:])
}

// fit shortens text with an ellipsis until it is no wider than width.
func fit(f font.Face, text string, width int) string {
	text = strings.TrimSpace(text)
	if font.MeasureString(f, text).Round() <= width {
		return text
	}
	runes := []rune(strings.TrimSuffix(text, "…"))
	for n := len(runes); n > 0; n-- {
		candidate := strings.TrimRight(string(runes[:n]), " ") + "…"
		if font.MeasureString(f, candidate).Round() <= width {
			return candidate
		}
	}
	return "…"
}

func measure(f font.Face, text string) int {
	return font.MeasureString(f, text).Round()
}
