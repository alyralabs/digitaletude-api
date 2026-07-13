// Package imageproc validates image uploads and produces thumbnails. Shared
// by photo originals and music cover art.
package imageproc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"

	"github.com/disintegration/imaging"
	"github.com/rwcarlsen/goexif/exif"
)

const (
	maxPixels    = 40_000_000
	thumbWidth   = 800
	thumbQuality = 80
)

var ErrUnsupportedType = errors.New("unsupported image type")

type Processed struct {
	Ext       string // "jpg" or "png", for the original's storage path
	MIME      string
	Width     int
	Height    int
	Thumbnail []byte // JPEG
}

// Process validates raw upload bytes and produces the thumbnail.
//
//   - Content type comes from magic bytes (http.DetectContentType), never the
//     client's header.
//   - image.DecodeConfig runs before full decode: a request-size cap does not
//     bound decoded memory, so oversized pixel counts are rejected up front.
//   - stdlib decoding ignores EXIF, so JPEG orientation is read explicitly and
//     applied before thumbnailing (phone portraits would otherwise come out
//     sideways). PNGs have no EXIF.
func Process(raw []byte) (*Processed, error) {
	mime := http.DetectContentType(raw)
	var ext string
	switch mime {
	case "image/jpeg":
		ext = "jpg"
	case "image/png":
		ext = "png"
	default:
		return nil, ErrUnsupportedType
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decoding image config: %w", err)
	}
	if cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("image too large: %dx%d exceeds %d pixels", cfg.Width, cfg.Height, maxPixels)
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	if mime == "image/jpeg" {
		img = applyOrientation(img, raw)
	}

	// Orientation 5-8 swap the axes, so measure after correcting.
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	thumb := img
	if width > thumbWidth {
		thumb = imaging.Resize(img, thumbWidth, 0, imaging.Lanczos)
	}
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, thumb, imaging.JPEG, imaging.JPEGQuality(thumbQuality)); err != nil {
		return nil, fmt.Errorf("encoding thumbnail: %w", err)
	}

	return &Processed{
		Ext:       ext,
		MIME:      mime,
		Width:     width,
		Height:    height,
		Thumbnail: buf.Bytes(),
	}, nil
}

// applyOrientation rotates/flips per the EXIF orientation tag. Any failure to
// read EXIF means "leave the image alone" — never fail an upload over it.
func applyOrientation(img image.Image, raw []byte) image.Image {
	x, err := exif.Decode(bytes.NewReader(raw))
	if err != nil {
		return img
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return img
	}
	orientation, err := tag.Int(0)
	if err != nil {
		return img
	}

	switch orientation {
	case 2:
		return imaging.FlipH(img)
	case 3:
		return imaging.Rotate180(img)
	case 4:
		return imaging.FlipV(img)
	case 5:
		return imaging.Transpose(img)
	case 6:
		return imaging.Rotate270(img) // 90° clockwise
	case 7:
		return imaging.Transverse(img)
	case 8:
		return imaging.Rotate90(img) // 90° counter-clockwise
	default:
		return img
	}
}
