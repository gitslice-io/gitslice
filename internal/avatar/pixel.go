// Package avatar draws and prepares account pictures: a pixel-art picture
// generated from the account's name for accounts without an uploaded one, and
// the normalized square PNG an uploaded picture is stored as.
package avatar

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
)

const (
	// spriteSize is the sprite's width and height in pixel-art cells. The
	// picture adds two empty cells of margin on every side, so the whole
	// sprite stays inside the circle a round avatar is cropped to.
	spriteSize = 8
	margin     = 2
	gridSize   = spriteSize + 2*margin
	cellPixels = 20
	// PixelSize is the generated picture's width and height in pixels.
	PixelSize = gridSize * cellPixels
)

// Pixel draws the generated picture of the named account as a PNG. The same
// name always gives the same picture (names are compared case-insensitively),
// so it needs no storage: an 8x8 sprite, mirrored left to right like a face,
// in two colors on a pale background, all picked from a hash of the name.
func Pixel(account string) []byte {
	sum := sha256.Sum256([]byte("gitslice-avatar:" + strings.ToLower(strings.TrimSpace(account))))
	cells := spriteCells(sum)

	hue := float64(uint16(sum[0])<<8|uint16(sum[1])) / 65536 * 360
	main := hsl(hue, 0.55+float64(sum[2]%20)/100, 0.42+float64(sum[3]%12)/100)
	// The accent sits a third or so of the way round the color wheel, on
	// either side, so pictures with near hues still differ.
	accentShift := 100 + float64(sum[4]%60)
	if sum[5]&1 == 1 {
		accentShift = -accentShift
	}
	accent := hsl(math.Mod(hue+accentShift+360, 360), 0.65, 0.58)
	background := hsl(hue, 0.45, 0.93)

	palette := color.Palette{background, main, accent}
	img := image.NewPaletted(image.Rect(0, 0, PixelSize, PixelSize), palette)
	for row := 0; row < spriteSize; row++ {
		for col := 0; col < spriteSize; col++ {
			value := cells[row][col]
			if value == 0 {
				continue
			}
			x0, y0 := (col+margin)*cellPixels, (row+margin)*cellPixels
			for y := y0; y < y0+cellPixels; y++ {
				for x := x0; x < x0+cellPixels; x++ {
					img.SetColorIndex(x, y, value)
				}
			}
		}
	}
	var out bytes.Buffer
	// Encoding a small paletted image in memory does not fail.
	_ = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img)
	return out.Bytes()
}

// spriteCells picks the sprite: 0 is background, 1 the main color, 2 the
// accent. The left half comes from the hash and the right half mirrors it.
func spriteCells(sum [32]byte) [spriteSize][spriteSize]uint8 {
	var cells [spriteSize][spriteSize]uint8
	half := spriteSize / 2
	filled := 0
	// The colors use the name's hash; the cells use a hash of that, one byte
	// per cell of the left half (8 rows x 4 columns = 32 bytes).
	shape := sha256.Sum256(sum[:])
	for row := 0; row < spriteSize; row++ {
		for col := 0; col < half; col++ {
			b := shape[row*half+col]
			switch {
			case b < 112: // about 44% background
			case b < 220: // about 42% main
				cells[row][col] = 1
				filled++
			default: // about 14% accent
				cells[row][col] = 2
				filled++
			}
		}
	}
	// A nearly empty or nearly full sprite reads as a blob, not a figure:
	// swap background and main color to bring it back toward half.
	total := spriteSize * half
	if filled < total/4 || filled > total*3/4 {
		for row := range cells {
			for col := 0; col < half; col++ {
				switch cells[row][col] {
				case 0:
					cells[row][col] = 1
				case 1:
					cells[row][col] = 0
				}
			}
		}
	}
	for row := range cells {
		for col := 0; col < half; col++ {
			cells[row][spriteSize-1-col] = cells[row][col]
		}
	}
	return cells
}

func hsl(h, s, l float64) color.RGBA {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	to8 := func(v float64) uint8 { return uint8(math.Round((v + m) * 255)) }
	return color.RGBA{R: to8(r), G: to8(g), B: to8(b), A: 255}
}
