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
	"math"
	"net/http"
	"strconv"
	"strings"

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

	// Exif is nil for PNGs, for JPEGs with no EXIF segment, and for JPEGs
	// whose EXIF has none of the fields below — never a reason to fail the
	// upload either way.
	Exif *Exif
}

// Exif is the small subset of EXIF metadata shown to visitors as a
// viewfinder-style overlay — not a full EXIF dump. Every field is optional:
// cameras vary in what they record, and a missing/malformed tag is simply
// omitted rather than failing extraction. Fields are pre-formatted strings
// (not raw numbers) so the frontend just joins them, no per-field
// formatting logic on that side.
type Exif struct {
	Camera       string `json:"camera,omitempty"`
	Aperture     string `json:"aperture,omitempty"`
	ShutterSpeed string `json:"shutterSpeed,omitempty"`
	ISO          string `json:"iso,omitempty"`
	FocalLength  string `json:"focalLength,omitempty"`
}

func (e *Exif) isEmpty() bool {
	return e.Camera == "" && e.Aperture == "" && e.ShutterSpeed == "" && e.ISO == "" && e.FocalLength == ""
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

	var exifData *Exif
	if mime == "image/jpeg" {
		if x, err := exif.Decode(bytes.NewReader(raw)); err == nil {
			img = applyOrientation(img, x)
			exifData = extractExif(x)
		}
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
		Exif:      exifData,
	}, nil
}

// applyOrientation rotates/flips per the EXIF orientation tag. Any failure to
// read the tag means "leave the image alone" — never fail an upload over it.
func applyOrientation(img image.Image, x *exif.Exif) image.Image {
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

// extractExif pulls the camera-settings fields shown in the viewfinder
// overlay. Each field is independent — one missing/malformed tag doesn't
// block the others. Returns nil if nothing at all was found, so callers can
// treat "no EXIF worth showing" as a single nil check.
func extractExif(x *exif.Exif) *Exif {
	e := &Exif{}

	cameraMake, _ := stringTag(x, exif.Make)
	model, _ := stringTag(x, exif.Model)
	e.Camera = combineCameraName(cameraMake, model)

	if num, den, err := ratTag(x, exif.FNumber); err == nil && den != 0 {
		e.Aperture = formatAperture(float64(num) / float64(den))
	}
	if num, den, err := ratTag(x, exif.ExposureTime); err == nil && den != 0 {
		e.ShutterSpeed = formatShutterSpeed(num, den)
	}
	if tag, err := x.Get(exif.ISOSpeedRatings); err == nil {
		if iso, err := tag.Int(0); err == nil {
			e.ISO = fmt.Sprintf("ISO %d", iso)
		}
	}
	if num, den, err := ratTag(x, exif.FocalLength); err == nil && den != 0 {
		e.FocalLength = formatFocalLength(float64(num) / float64(den))
	}

	if e.isEmpty() {
		return nil
	}
	return e
}

func stringTag(x *exif.Exif, name exif.FieldName) (string, error) {
	tag, err := x.Get(name)
	if err != nil {
		return "", err
	}
	return tag.StringVal()
}

func ratTag(x *exif.Exif, name exif.FieldName) (num, den int64, err error) {
	tag, err := x.Get(name)
	if err != nil {
		return 0, 0, err
	}
	return tag.Rat2(0)
}

// combineCameraName joins make + model, deduping when the model already
// carries the brand. Full-string containment handles "Canon" + "Canon EOS
// R5"; the first-word comparison handles corporate-suffix makes like
// "NIKON CORPORATION" + "NIKON D850", which are the majority shape among
// the big vendors.
func combineCameraName(cameraMake, model string) string {
	cameraMake = strings.TrimSpace(cameraMake)
	model = strings.TrimSpace(model)
	if model == "" {
		return cameraMake
	}
	if cameraMake == "" {
		return model
	}
	makeBrand := strings.ToLower(strings.Fields(cameraMake)[0])
	modelBrand := strings.ToLower(strings.Fields(model)[0])
	if modelBrand == makeBrand ||
		strings.Contains(strings.ToLower(model), strings.ToLower(cameraMake)) {
		return model
	}
	return cameraMake + " " + model
}

func formatAperture(f float64) string {
	return "f/" + trimTrailingZero(f)
}

func formatFocalLength(mm float64) string {
	return trimTrailingZero(mm) + "mm"
}

// trimTrailingZero formats to one decimal place, then drops a trailing
// ".0" — f/2.8 stays f/2.8, f/11.0 becomes f/11.
func trimTrailingZero(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(strings.TrimSuffix(s, "0"), ".")
}

// formatShutterSpeed renders sub-second exposures as "1/x s" when the value
// genuinely is (within tolerance) a unit fraction — covering both unreduced
// rationals like 2/500 and decimal-rational storage like 333333/1000000 —
// and as decimal seconds otherwise, matching cameras' own convention for
// slow shutters (0.7s displays as 0"7, never as a rounded-wrong "1/1").
// Exposures of a second or longer are always decimal seconds.
func formatShutterSpeed(num, den int64) string {
	if num == 1 && den > 1 {
		return fmt.Sprintf("1/%ds", den)
	}
	value := float64(num) / float64(den)
	if value <= 0 {
		return ""
	}
	if value >= 1 {
		return trimTrailingZero(value) + "s"
	}
	reciprocal := math.Round(1 / value)
	if math.Abs(1/reciprocal-value) <= value*0.02 {
		return fmt.Sprintf("1/%.0fs", reciprocal)
	}
	return trimTrailingZero(value) + "s"
}
