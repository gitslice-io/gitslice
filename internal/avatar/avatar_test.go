package avatar

import (
	"bytes"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestPixelIsStablePerNameAndDiffersBetweenNames(t *testing.T) {
	if !bytes.Equal(Pixel("sunny"), Pixel("sunny")) {
		t.Fatal("the same name drew two different pictures")
	}
	if !bytes.Equal(Pixel("Sunny"), Pixel(" sunny ")) {
		t.Fatal("names should compare case-insensitively and without spaces")
	}
	seen := map[string]string{}
	for _, name := range []string{"alice", "bob", "carol", "dave", "erin", "frank", "gitslice", "slices", "nic", "sunny"} {
		key := string(Pixel(name))
		if other, ok := seen[key]; ok {
			t.Fatalf("%s and %s drew the same picture", name, other)
		}
		seen[key] = name
	}
}

func TestPixelIsAMirroredSpriteInsideItsMargin(t *testing.T) {
	for _, name := range []string{"alice", "bob", "gitslice", "x"} {
		img, err := png.Decode(bytes.NewReader(Pixel(name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if img.Bounds().Dx() != PixelSize || img.Bounds().Dy() != PixelSize {
			t.Fatalf("%s: size %v, want %dx%d", name, img.Bounds(), PixelSize, PixelSize)
		}
		background := img.At(0, 0)
		filled := 0
		for row := 0; row < gridSize; row++ {
			for col := 0; col < gridSize; col++ {
				c := cellColor(img, row, col)
				mirror := cellColor(img, row, gridSize-1-col)
				if c != mirror {
					t.Fatalf("%s: cell %d,%d is not mirrored", name, row, col)
				}
				inMargin := row < margin || col < margin || row >= gridSize-margin || col >= gridSize-margin
				if inMargin && c != background {
					t.Fatalf("%s: margin cell %d,%d is drawn", name, row, col)
				}
				if c != background {
					filled++
				}
			}
		}
		cells := spriteSize * spriteSize
		if filled < cells/4 || filled > cells*3/4 {
			t.Fatalf("%s: %d of %d cells drawn; want between a quarter and three quarters", name, filled, cells)
		}
	}
}

func cellColor(img image.Image, row, col int) color.Color {
	return img.At(col*cellPixels+cellPixels/2, row*cellPixels+cellPixels/2)
}

func TestNormalizeCropsTheCenterSquareAndScalesIt(t *testing.T) {
	// 600x300: red left quarter, green middle half, blue right quarter. The
	// centered 300x300 square is all green.
	src := image.NewRGBA(image.Rect(0, 0, 600, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 600; x++ {
			c := color.RGBA{G: 255, A: 255}
			if x < 150 {
				c = color.RGBA{R: 255, A: 255}
			} else if x >= 450 {
				c = color.RGBA{B: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}
	var in bytes.Buffer
	if err := jpeg.Encode(&in, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	out, err := Normalize(in.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "png" {
		t.Fatalf("stored picture: format %q, err %v; want png", format, err)
	}
	if img.Bounds().Dx() != UploadSize || img.Bounds().Dy() != UploadSize {
		t.Fatalf("size %v, want %dx%d", img.Bounds(), UploadSize, UploadSize)
	}
	for _, p := range []image.Point{{2, 2}, {UploadSize - 3, UploadSize / 2}} {
		r, g, b, _ := img.At(p.X, p.Y).RGBA()
		if g>>8 < 200 || r>>8 > 60 || b>>8 > 60 {
			t.Fatalf("pixel %v = %d,%d,%d; want green (the centered square)", p, r>>8, g>>8, b>>8)
		}
	}
}

func TestNormalizeKeepsASmallPictureItsSize(t *testing.T) {
	var in bytes.Buffer
	if err := png.Encode(&in, image.NewRGBA(image.Rect(0, 0, 64, 80))); err != nil {
		t.Fatal(err)
	}
	out, err := Normalize(in.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || config.Width != 64 || config.Height != 64 {
		t.Fatalf("got %dx%d (%v), want 64x64", config.Width, config.Height, err)
	}
}

func TestNormalizeRejectsWhatIsNotAPicture(t *testing.T) {
	huge := hugePNGHeader(t, 20_000, 20_000)
	for name, data := range map[string][]byte{
		"empty":      nil,
		"text":       []byte("hello"),
		"svg":        []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"too large":  bytes.Repeat([]byte{0}, MaxUploadBytes+1),
		"pixel bomb": huge,
	} {
		if _, err := Normalize(data); !errors.Is(err, ErrNotPicture) {
			t.Errorf("%s: err = %v, want ErrNotPicture", name, err)
		}
	}
	// The size limit refuses the bomb before decoding allocates its pixels.
	if _, err := Normalize(huge); err == nil || !strings.Contains(err.Error(), "megapixels") {
		t.Errorf("pixel bomb: err = %v, want the megapixel limit", err)
	}
}

// hugePNGHeader is a small PNG file whose header claims width x height.
func hugePNGHeader(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	// IHDR's width and height follow the 8-byte signature, 4-byte length and
	// 4-byte type; its CRC (over type and data) follows its 13 data bytes.
	put := func(at int, v uint32) {
		data[at], data[at+1], data[at+2], data[at+3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
	}
	put(16, uint32(width))
	put(20, uint32(height))
	put(29, crc32.ChecksumIEEE(data[12:29]))
	if config, err := png.DecodeConfig(bytes.NewReader(data)); err != nil || config.Width != width {
		t.Fatalf("crafted header does not decode: %v", err)
	}
	return data
}
