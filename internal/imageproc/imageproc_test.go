package imageproc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func solidImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	return img
}

func encodeJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, solidImage(width, height), nil); err != nil {
		t.Fatalf("encoding test JPEG: %v", err)
	}
	return buf.Bytes()
}

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, solidImage(width, height)); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}
	return buf.Bytes()
}

// exifJPEG splices a minimal hand-built EXIF APP1 segment (single
// Orientation tag) into an otherwise-valid JPEG, right after the SOI
// marker, so applyOrientation has something real to read.
func exifJPEG(t *testing.T, width, height int, orientation uint16) []byte {
	t.Helper()
	base := encodeJPEG(t, width, height)

	tiff := new(bytes.Buffer)
	tiff.WriteString("II")                              // little-endian
	binary.Write(tiff, binary.LittleEndian, uint16(42))  // TIFF magic
	binary.Write(tiff, binary.LittleEndian, uint32(8))   // offset to IFD0
	binary.Write(tiff, binary.LittleEndian, uint16(1))   // 1 entry
	binary.Write(tiff, binary.LittleEndian, uint16(0x0112)) // tag: Orientation
	binary.Write(tiff, binary.LittleEndian, uint16(3))   // type: SHORT
	binary.Write(tiff, binary.LittleEndian, uint32(1))   // count
	binary.Write(tiff, binary.LittleEndian, orientation) // value (+2 pad bytes)
	binary.Write(tiff, binary.LittleEndian, uint16(0))
	binary.Write(tiff, binary.LittleEndian, uint32(0)) // next IFD offset

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)

	segment := new(bytes.Buffer)
	segment.Write([]byte{0xFF, 0xE1})
	binary.Write(segment, binary.BigEndian, uint16(len(payload)+2))
	segment.Write(payload)

	out := new(bytes.Buffer)
	out.Write(base[:2]) // SOI
	out.Write(segment.Bytes())
	out.Write(base[2:])
	return out.Bytes()
}

// bombPNG builds a truncated but valid-enough PNG (signature + one IHDR
// chunk) declaring huge dimensions. image.DecodeConfig only needs IHDR to
// report a config, so Process's bomb guard should reject this before ever
// attempting a full decode of a file that has no pixel data at all.
func bombPNG(width, height uint32) []byte {
	sig := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

	data := new(bytes.Buffer)
	binary.Write(data, binary.BigEndian, width)
	binary.Write(data, binary.BigEndian, height)
	data.Write([]byte{8, 6, 0, 0, 0}) // bit depth, color type (RGBA), compression, filter, interlace

	chunkType := []byte("IHDR")
	crc := crc32.ChecksumIEEE(append(append([]byte{}, chunkType...), data.Bytes()...))

	ihdr := new(bytes.Buffer)
	binary.Write(ihdr, binary.BigEndian, uint32(data.Len()))
	ihdr.Write(chunkType)
	ihdr.Write(data.Bytes())
	binary.Write(ihdr, binary.BigEndian, crc)

	return append(sig, ihdr.Bytes()...)
}

func decodedWidth(t *testing.T, jpegBytes []byte) int {
	t.Helper()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpegBytes))
	if err != nil {
		t.Fatalf("decoding thumbnail: %v", err)
	}
	return cfg.Width
}

func TestProcess_AcceptsValidJPEGAndPNG(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		wantExt string
		wantMIME string
	}{
		{"jpeg", encodeJPEG(t, 100, 80), "jpg", "image/jpeg"},
		{"png", encodePNG(t, 100, 80), "png", "image/png"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc, err := Process(tc.raw)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if proc.Ext != tc.wantExt {
				t.Errorf("Ext = %q, want %q", proc.Ext, tc.wantExt)
			}
			if proc.MIME != tc.wantMIME {
				t.Errorf("MIME = %q, want %q", proc.MIME, tc.wantMIME)
			}
			if proc.Width != 100 || proc.Height != 80 {
				t.Errorf("dimensions = %dx%d, want 100x80", proc.Width, proc.Height)
			}
		})
	}
}

func TestProcess_RejectsUnsupportedType(t *testing.T) {
	_, err := Process([]byte("this is not an image, just plain text padding out to be long enough"))
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestProcess_RejectsPixelBomb(t *testing.T) {
	// 10000x10000 = 100,000,000 pixels, well over the 40MP cap.
	_, err := Process(bombPNG(10000, 10000))
	if err == nil {
		t.Fatal("Process() succeeded on a pixel-bomb PNG, want an error")
	}
	if errors.Is(err, ErrUnsupportedType) {
		t.Error("bomb PNG was rejected as unsupported type, not as too-large — DetectContentType or the guard itself is off")
	}
}

func TestProcess_AllowsImageUnderPixelCap(t *testing.T) {
	// Sanity check the bomb guard's threshold isn't rejecting normal images.
	// Unlike bombPNG (header-only, deliberately undecodable), this is a
	// complete, real PNG so it exercises the full decode path too.
	_, err := Process(encodePNG(t, 1000, 1000))
	if err != nil {
		t.Fatalf("Process() rejected a 1,000,000 pixel image: %v", err)
	}
}

func TestProcess_CorrectsJPEGOrientation(t *testing.T) {
	// Orientation 6 = rotate 90 CW to display correctly, i.e. the source
	// bytes are stored rotated 90 CCW from upright. A 100x80 (wide) source
	// tagged orientation 6 should come out 80x100 (tall) after correction.
	raw := exifJPEG(t, 100, 80, 6)

	proc, err := Process(raw)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Width != 80 || proc.Height != 100 {
		t.Errorf("dimensions after orientation fix = %dx%d, want 80x100 (swapped)", proc.Width, proc.Height)
	}
}

func TestProcess_PNGIgnoresOrientation(t *testing.T) {
	// PNGs have no EXIF; dimensions should pass through unchanged regardless
	// of content — nothing to swap.
	proc, err := Process(encodePNG(t, 100, 80))
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Width != 100 || proc.Height != 80 {
		t.Errorf("dimensions = %dx%d, want 100x80 (unchanged)", proc.Width, proc.Height)
	}
}

func TestProcess_ThumbnailIsResizedWhenWide(t *testing.T) {
	proc, err := Process(encodeJPEG(t, 1600, 800))
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got := decodedWidth(t, proc.Thumbnail); got != thumbWidth {
		t.Errorf("thumbnail width = %d, want %d", got, thumbWidth)
	}
}

func TestProcess_ThumbnailNotUpscaledWhenNarrow(t *testing.T) {
	proc, err := Process(encodeJPEG(t, 400, 300))
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got := decodedWidth(t, proc.Thumbnail); got != 400 {
		t.Errorf("thumbnail width = %d, want 400 (unchanged, not upscaled to %d)", got, thumbWidth)
	}
}
