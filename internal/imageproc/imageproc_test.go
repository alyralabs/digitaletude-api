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
	tiff.WriteString("II")                                  // little-endian
	binary.Write(tiff, binary.LittleEndian, uint16(42))     // TIFF magic
	binary.Write(tiff, binary.LittleEndian, uint32(8))      // offset to IFD0
	binary.Write(tiff, binary.LittleEndian, uint16(1))      // 1 entry
	binary.Write(tiff, binary.LittleEndian, uint16(0x0112)) // tag: Orientation
	binary.Write(tiff, binary.LittleEndian, uint16(3))      // type: SHORT
	binary.Write(tiff, binary.LittleEndian, uint32(1))      // count
	binary.Write(tiff, binary.LittleEndian, orientation)    // value (+2 pad bytes)
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

// ifdEntry describes one TIFF IFD0 entry for exifJPEGWithTags. value holds
// the raw bytes for the tag's data; if it's 4 bytes or less it's inlined
// in the entry itself (TIFF's rule), otherwise it's appended after the IFD
// and the entry stores an offset to it instead.
type ifdEntry struct {
	tag   uint16
	typ   uint16
	count uint32
	value []byte
}

func asciiValue(s string) []byte { return append([]byte(s), 0) }

func shortValue(v uint16) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func rationalValue(num, den uint32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b[0:4], num)
	binary.LittleEndian.PutUint32(b[4:8], den)
	return b
}

// exifJPEGWithTags splices a hand-built EXIF APP1 segment with an arbitrary
// set of IFD0 entries into an otherwise-valid JPEG. Unlike exifJPEG (single
// inline SHORT tag), this handles external (>4 byte) values too — ASCII
// strings and RATIONALs — computing their offsets rather than hand-counting
// bytes, so it's not a error-prone arithmetic exercise per call site.
//
// goexif's field-name lookup is flat across whichever IFD a tag was found
// in (confirmed by reading exif.go: (*Exif).main is populated by the same
// exifFields map for IFD0 as for the Exif sub-IFD), so real EXIF's
// IFD0-vs-sub-IFD split doesn't need to be reproduced here — every tag
// below can live directly in IFD0.
func exifJPEGWithTags(t *testing.T, width, height int, entries []ifdEntry) []byte {
	t.Helper()
	base := encodeJPEG(t, width, height)

	const tiffHeaderLen = 8 // "II" + magic(2) + ifd0 offset(4)
	ifdStart := tiffHeaderLen
	ifdLen := 2 + len(entries)*12 + 4 // count + entries + next-IFD-offset
	externalStart := ifdStart + ifdLen

	var external bytes.Buffer
	offsets := make([]uint32, len(entries))
	for i, e := range entries {
		if len(e.value) > 4 {
			offsets[i] = uint32(externalStart + external.Len())
			external.Write(e.value)
			if external.Len()%2 == 1 {
				external.WriteByte(0) // keep the next entry word-aligned
			}
		}
	}

	tiff := new(bytes.Buffer)
	tiff.WriteString("II")
	binary.Write(tiff, binary.LittleEndian, uint16(42))
	binary.Write(tiff, binary.LittleEndian, uint32(ifdStart))
	binary.Write(tiff, binary.LittleEndian, uint16(len(entries)))
	for i, e := range entries {
		binary.Write(tiff, binary.LittleEndian, e.tag)
		binary.Write(tiff, binary.LittleEndian, e.typ)
		binary.Write(tiff, binary.LittleEndian, e.count)
		if len(e.value) > 4 {
			binary.Write(tiff, binary.LittleEndian, offsets[i])
		} else {
			padded := make([]byte, 4)
			copy(padded, e.value)
			tiff.Write(padded)
		}
	}
	binary.Write(tiff, binary.LittleEndian, uint32(0)) // next IFD offset
	tiff.Write(external.Bytes())

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
		name     string
		raw      []byte
		wantExt  string
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
	// 10000x10000 = 100,000,000 pixels, over the 80MP cap.
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

func TestProcessAt_UsesCallerWidth(t *testing.T) {
	proc, err := ProcessAt(encodeJPEG(t, 1600, 800), 320)
	if err != nil {
		t.Fatalf("ProcessAt() error = %v", err)
	}
	if got := decodedWidth(t, proc.Thumbnail); got != 320 {
		t.Errorf("thumbnail width = %d, want 320", got)
	}
	// Reported dimensions stay those of the original, not the thumbnail.
	if proc.Width != 1600 || proc.Height != 800 {
		t.Errorf("Width×Height = %d×%d, want 1600×800", proc.Width, proc.Height)
	}
}

func TestProcess_ExtractsExifCameraSettings(t *testing.T) {
	raw := exifJPEGWithTags(t, 100, 80, []ifdEntry{
		{tag: 0x010F, typ: 2, count: 6, value: asciiValue("Canon")},         // Make
		{tag: 0x0110, typ: 2, count: 13, value: asciiValue("Canon EOS R5")}, // Model
		{tag: 0x829A, typ: 5, count: 1, value: rationalValue(1, 250)},       // ExposureTime
		{tag: 0x829D, typ: 5, count: 1, value: rationalValue(28, 10)},       // FNumber
		{tag: 0x8827, typ: 3, count: 1, value: shortValue(400)},             // ISOSpeedRatings
		{tag: 0x920A, typ: 5, count: 1, value: rationalValue(50, 1)},        // FocalLength
	})

	proc, err := Process(raw)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Exif == nil {
		t.Fatal("Exif is nil, want extracted camera settings")
	}
	if proc.Exif.Camera != "Canon EOS R5" {
		t.Errorf("Camera = %q, want %q (deduped, not \"Canon Canon EOS R5\")", proc.Exif.Camera, "Canon EOS R5")
	}
	if proc.Exif.Aperture != "f/2.8" {
		t.Errorf("Aperture = %q, want %q", proc.Exif.Aperture, "f/2.8")
	}
	if proc.Exif.ShutterSpeed != "1/250s" {
		t.Errorf("ShutterSpeed = %q, want %q", proc.Exif.ShutterSpeed, "1/250s")
	}
	if proc.Exif.ISO != "ISO 400" {
		t.Errorf("ISO = %q, want %q", proc.Exif.ISO, "ISO 400")
	}
	if proc.Exif.FocalLength != "50mm" {
		t.Errorf("FocalLength = %q, want %q", proc.Exif.FocalLength, "50mm")
	}
}

func TestProcess_CombinesMakeAndModelWhenModelLacksMake(t *testing.T) {
	raw := exifJPEGWithTags(t, 100, 80, []ifdEntry{
		{tag: 0x010F, typ: 2, count: 6, value: asciiValue("Canon")},
		{tag: 0x0110, typ: 2, count: 6, value: asciiValue("EOS R5")}, // doesn't already contain "Canon"
	})

	proc, err := Process(raw)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Exif == nil || proc.Exif.Camera != "Canon EOS R5" {
		t.Errorf("Camera = %v, want %q", proc.Exif, "Canon EOS R5")
	}
}

func TestProcess_FormatsWholeNumberApertureWithoutDecimal(t *testing.T) {
	raw := exifJPEGWithTags(t, 100, 80, []ifdEntry{
		{tag: 0x829D, typ: 5, count: 1, value: rationalValue(110, 10)}, // f/11.0
	})

	proc, err := Process(raw)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Exif == nil || proc.Exif.Aperture != "f/11" {
		t.Errorf("Aperture = %v, want %q (no trailing .0)", proc.Exif, "f/11")
	}
}

func TestProcess_FormatsSlowShutterSpeedInSeconds(t *testing.T) {
	raw := exifJPEGWithTags(t, 100, 80, []ifdEntry{
		{tag: 0x829A, typ: 5, count: 1, value: rationalValue(2, 1)}, // 2 second exposure
	})

	proc, err := Process(raw)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Exif == nil || proc.Exif.ShutterSpeed != "2s" {
		t.Errorf("ShutterSpeed = %v, want %q", proc.Exif, "2s")
	}
}

func TestProcess_ExifNilWhenNoRecognizedFieldsPresent(t *testing.T) {
	// Orientation is real EXIF data, but not one of the fields the
	// viewfinder overlay shows — extraction should find nothing worth
	// surfacing and return nil, not a struct of empty strings.
	raw := exifJPEG(t, 100, 80, 1)

	proc, err := Process(raw)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Exif != nil {
		t.Errorf("Exif = %+v, want nil (only Orientation was present)", proc.Exif)
	}
}

func TestProcess_ExifNilWhenJPEGHasNoExifSegment(t *testing.T) {
	proc, err := Process(encodeJPEG(t, 100, 80))
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Exif != nil {
		t.Errorf("Exif = %+v, want nil (no EXIF segment at all)", proc.Exif)
	}
}

func TestProcess_ExifNilForPNG(t *testing.T) {
	proc, err := Process(encodePNG(t, 100, 80))
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if proc.Exif != nil {
		t.Errorf("Exif = %+v, want nil (PNGs have no EXIF)", proc.Exif)
	}
}

// Direct table tests for the two formatting helpers — the through-Process
// tests above cover wiring; these cover the value-space edge cases that a
// full JPEG fixture per case would make prohibitively verbose. Same
// pure-function-table pattern as posts' slug_test.go.

func TestFormatShutterSpeed(t *testing.T) {
	cases := []struct {
		name     string
		num, den int64
		want     string
	}{
		{"fast, stored as unit fraction", 1, 250, "1/250s"},
		{"fast, stored unreduced", 2, 500, "1/250s"},
		{"one third stored as decimal rational", 333333, 1000000, "1/3s"},
		{"half second", 5, 10, "1/2s"},
		{"0.7s long exposure is not 1/1s", 7, 10, "0.7s"},
		{"0.6s long exposure is not 1/2s", 6, 10, "0.6s"},
		{"exactly one second", 1, 1, "1s"},
		{"multi-second", 2, 1, "2s"},
		{"fractional seconds over one", 15, 10, "1.5s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatShutterSpeed(tc.num, tc.den); got != tc.want {
				t.Errorf("formatShutterSpeed(%d, %d) = %q, want %q", tc.num, tc.den, got, tc.want)
			}
		})
	}
}

func TestCombineCameraName(t *testing.T) {
	cases := []struct {
		name        string
		make, model string
		want        string
	}{
		{"model already contains full make", "Canon", "Canon EOS R5", "Canon EOS R5"},
		{"corporate-suffix make dedups by brand word", "NIKON CORPORATION", "NIKON D850", "NIKON D850"},
		{"another corporate suffix", "OLYMPUS IMAGING CORP.", "OLYMPUS E-M1", "OLYMPUS E-M1"},
		{"model lacks brand entirely", "SONY", "ILCE-7M4", "SONY ILCE-7M4"},
		{"make only", "Canon", "", "Canon"},
		{"model only", "", "EOS R5", "EOS R5"},
		{"case-insensitive dedup", "canon", "Canon EOS R5", "Canon EOS R5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := combineCameraName(tc.make, tc.model); got != tc.want {
				t.Errorf("combineCameraName(%q, %q) = %q, want %q", tc.make, tc.model, got, tc.want)
			}
		})
	}
}
