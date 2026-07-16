package photos

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alyralabs/digitaletude-api/internal/storage"
	"github.com/alyralabs/digitaletude-api/internal/testutil"
)

// newTestServer wires a real Handler (real repo, transaction-backed) behind
// real RegisterPublic/RegisterAdmin — the same routing code that runs in
// production — plus an httptest-mocked storage backend so no real Supabase
// Storage call is ever made.
func newTestServer(t *testing.T) *http.ServeMux {
	t.Helper()
	repo := NewRepo(testutil.OpenTestTx(t))

	storageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(storageSrv.Close)
	st := storage.New(storageSrv.URL, "test-secret")

	h := NewHandler(repo, st)
	mux := http.NewServeMux()
	h.RegisterPublic(mux)
	h.RegisterAdmin(mux)
	return mux
}

// newFailingStorageTestServer is a variant of newTestServer whose mock
// storage backend fails DELETE requests only — uploads still succeed, so a
// test can create a photo normally and then exercise what happens when
// storage cleanup fails on delete, without the setup step itself failing.
func newFailingStorageTestServer(t *testing.T) *http.ServeMux {
	t.Helper()
	repo := NewRepo(testutil.OpenTestTx(t))

	storageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(storageSrv.Close)
	st := storage.New(storageSrv.URL, "test-secret")

	h := NewHandler(repo, st)
	mux := http.NewServeMux()
	h.RegisterPublic(mux)
	h.RegisterAdmin(mux)
	return mux
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 100, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encoding test JPEG: %v", err)
	}
	return buf.Bytes()
}

// isoJPEG splices a minimal hand-built EXIF APP1 segment (single inline
// ISOSpeedRatings tag) into an otherwise-valid JPEG — just enough to prove
// the handler wires imageproc's extraction through to the stored/returned
// photo end to end. Full extraction correctness (every field, external
// RATIONAL/ASCII values, dedup, formatting) is imageproc's own test
// suite's job, not re-tested here.
func isoJPEG(t *testing.T, width, height int, iso uint16) []byte {
	t.Helper()
	base := testJPEGSized(t, width, height)

	tiff := new(bytes.Buffer)
	tiff.WriteString("II")
	binary.Write(tiff, binary.LittleEndian, uint16(42))
	binary.Write(tiff, binary.LittleEndian, uint32(8))
	binary.Write(tiff, binary.LittleEndian, uint16(1))
	binary.Write(tiff, binary.LittleEndian, uint16(0x8827)) // tag: ISOSpeedRatings
	binary.Write(tiff, binary.LittleEndian, uint16(3))      // type: SHORT
	binary.Write(tiff, binary.LittleEndian, uint32(1))      // count
	binary.Write(tiff, binary.LittleEndian, iso)
	binary.Write(tiff, binary.LittleEndian, uint16(0)) // pad to 4 bytes
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

func testJPEGSized(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encoding test JPEG: %v", err)
	}
	return buf.Bytes()
}

func multipartUploadBody(t *testing.T, title, description string, fileBytes []byte) (*bytes.Buffer, string) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	if err := w.WriteField("title", title); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("description", description); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("file", "test.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(fileBytes); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf, w.FormDataContentType()
}

func TestHandler_PublicRoutes(t *testing.T) {
	mux := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/photos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/photos status = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/photos/00000000-0000-0000-0000-000000000000", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/photos/{unknown-id} status = %d, want 404", rec.Code)
	}
}

func TestHandler_Create_RejectsNonImage(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartUploadBody(t, "Bad Upload", "", []byte("this is not an image at all"))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestHandler_Create_RejectsOversizedBody(t *testing.T) {
	mux := newTestServer(t)
	oversized := bytes.Repeat([]byte{0}, 51<<20) // 51 MiB, over the 50 MiB cap
	body, contentType := multipartUploadBody(t, "Too Big", "", oversized)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestHandler_Create_Success(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartUploadBody(t, "A Real Photo", "a description", testJPEG(t))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}

	var got Photo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Title != "A Real Photo" {
		t.Errorf("Title = %q, want %q", got.Title, "A Real Photo")
	}
	if got.OriginalURL == "" || got.ThumbnailURL == "" {
		t.Errorf("expected composed URLs, got originalUrl=%q thumbnailUrl=%q", got.OriginalURL, got.ThumbnailURL)
	}
	if got.Exif != nil {
		t.Errorf("Exif = %s, want nil/omitted for a JPEG with no EXIF segment", got.Exif)
	}
}

func TestHandler_Create_ExtractsAndStoresExif(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartUploadBody(t, "Has Exif", "", isoJPEG(t, 100, 80, 400))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	var got Photo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Exif == nil {
		t.Fatal("Exif is nil, want the extracted ISO setting")
	}
	var exif struct {
		ISO string `json:"iso"`
	}
	if err := json.Unmarshal(got.Exif, &exif); err != nil {
		t.Fatalf("decoding exif field: %v", err)
	}
	if exif.ISO != "ISO 400" {
		t.Errorf("exif.iso = %q, want %q", exif.ISO, "ISO 400")
	}

	// Confirmed stored, not just echoed back on the create response.
	getReq := httptest.NewRequest(http.MethodGet, "/api/photos/"+got.ID, nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/photos/%s status = %d, want 200, body: %s", got.ID, getRec.Code, getRec.Body.String())
	}
	var fetched Photo
	if err := json.Unmarshal(getRec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decoding re-fetched photo: %v", err)
	}
	if fetched.Exif == nil {
		t.Error("re-fetched photo's Exif is nil, want it persisted")
	}
}

func TestHandler_Delete_RemovesRowEvenIfStorageCleanupFails(t *testing.T) {
	mux := newFailingStorageTestServer(t)
	body, contentType := multipartUploadBody(t, "Will Be Deleted", "", testJPEG(t))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: create status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	var created Photo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/photos/"+created.ID, nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 even though storage cleanup fails, body: %s", delRec.Code, delRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/photos/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Errorf("GET after delete status = %d, want 404 (row must be gone despite failed storage cleanup)", getRec.Code)
	}
}

func TestHandler_UnknownID_Returns404NotFor500(t *testing.T) {
	mux := newTestServer(t)
	unknownID := "00000000-0000-0000-0000-000000000000"

	newTitle := "won't apply"
	patchBody, _ := json.Marshal(PhotoUpdate{Title: &newTitle})
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/admin/photos/"+unknownID, bytes.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	mux.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusNotFound {
		t.Errorf("PATCH unknown id status = %d, want 404", patchRec.Code)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/photos/"+unknownID, nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown id status = %d, want 404", delRec.Code)
	}
}
