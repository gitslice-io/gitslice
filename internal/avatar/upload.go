package avatar

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // Uploads may be GIF (its first frame is kept).
	_ "image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// MaxUploadBytes is the largest picture file accepted.
	MaxUploadBytes = 5 << 20
	// maxSourcePixels bounds the decoded picture, so a small file that
	// claims a huge size cannot make the server allocate gigabytes.
	maxSourcePixels = 40_000_000
	// UploadSize is the width and height an uploaded picture is stored at.
	UploadSize = 256
)

// ErrNotPicture is returned for data that is not a PNG, JPEG, GIF or WebP
// picture, or one too large to accept.
var ErrNotPicture = errors.New("not a picture")

// Normalize turns an uploaded picture into the PNG stored for it: the
// largest centered square, scaled to UploadSize. Re-encoding drops whatever
// else the file carried (metadata, extra frames, trailing bytes), so only
// pixels are ever served back.
func Normalize(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: the file is empty", ErrNotPicture)
	}
	if len(data) > MaxUploadBytes {
		return nil, fmt.Errorf("%w: the file is larger than %d MB", ErrNotPicture, MaxUploadBytes>>20)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: use a PNG, JPEG, GIF or WebP file", ErrNotPicture)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > maxSourcePixels {
		return nil, fmt.Errorf("%w: the picture is %dx%d pixels; the limit is %d megapixels", ErrNotPicture, config.Width, config.Height, maxSourcePixels/1_000_000)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: the file could not be read as a picture", ErrNotPicture)
	}
	bounds := src.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	crop := image.Rect(0, 0, side, side).Add(image.Pt(
		bounds.Min.X+(bounds.Dx()-side)/2,
		bounds.Min.Y+(bounds.Dy()-side)/2,
	))
	size := min(side, UploadSize)
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
