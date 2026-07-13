package photos

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/alyralabs/digitaletude-api/internal/auth"
	"github.com/alyralabs/digitaletude-api/internal/storage"
	"github.com/alyralabs/digitaletude-api/internal/testutil"
)

const testAdminID = "11111111-1111-1111-1111-111111111111"

// newTestServer wires a real Handler (real repo, transaction-backed) behind
// a real Register(mux, adminWrap) — the same auth-gating and routing code
// that runs in production — plus an httptest-mocked storage backend so no
// real Supabase Storage call is ever made. Returns the mux and a valid
// admin bearer token signed against the same verifier the mux uses.
func newTestServer(t *testing.T) (mux *http.ServeMux, adminToken string) {
	t.Helper()
	repo := NewRepo(testutil.OpenTestTx(t))

	storageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(storageSrv.Close)
	st := storage.New(storageSrv.URL, "test-secret")

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kf := func(*jwt.Token) (any, error) { return &priv.PublicKey, nil }
	verifier := auth.NewVerifierWithKeyfunc(kf, testAdminID)

	claims := jwt.MapClaims{
		"aud":  "authenticated",
		"role": "authenticated",
		"sub":  testAdminID,
		"exp":  time.Now().Add(time.Hour).Unix(),
		"iat":  time.Now().Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}

	mux = http.NewServeMux()
	NewHandler(repo, st).Register(mux, verifier.Middleware)
	return mux, token
}

// newFailingStorageTestServer is a variant of newTestServer whose mock
// storage backend fails DELETE requests only — uploads still succeed, so a
// test can create a photo normally and then exercise what happens when
// storage cleanup fails on delete, without the setup step itself failing.
func newFailingStorageTestServer(t *testing.T) (mux *http.ServeMux, adminToken string) {
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

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kf := func(*jwt.Token) (any, error) { return &priv.PublicKey, nil }
	verifier := auth.NewVerifierWithKeyfunc(kf, testAdminID)
	claims := jwt.MapClaims{
		"aud": "authenticated", "role": "authenticated", "sub": testAdminID,
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}

	mux = http.NewServeMux()
	NewHandler(repo, st).Register(mux, verifier.Middleware)
	return mux, token
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

func TestHandler_PublicRoutesRequireNoAuth(t *testing.T) {
	mux, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/photos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/photos (no token) status = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/photos/00000000-0000-0000-0000-000000000000", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/photos/{unknown-id} (no token) status = %d, want 404 (not 401 — route must stay public)", rec.Code)
	}
}

func TestHandler_AdminRoutesRejectMissingToken(t *testing.T) {
	mux, _ := newTestServer(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/admin/photos"},
		{http.MethodPatch, "/api/admin/photos/00000000-0000-0000-0000-000000000000"},
		{http.MethodDelete, "/api/admin/photos/00000000-0000-0000-0000-000000000000"},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s (no token) status = %d, want 401", tc.method, tc.path, rec.Code)
			}
		})
	}
}

func TestHandler_Create_RejectsNonImage(t *testing.T) {
	mux, token := newTestServer(t)
	body, contentType := multipartUploadBody(t, "Bad Upload", "", []byte("this is not an image at all"))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestHandler_Create_RejectsOversizedBody(t *testing.T) {
	mux, token := newTestServer(t)
	oversized := bytes.Repeat([]byte{0}, 16<<20) // 16 MiB, over the 15 MiB cap
	body, contentType := multipartUploadBody(t, "Too Big", "", oversized)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestHandler_Create_Success(t *testing.T) {
	mux, token := newTestServer(t)
	body, contentType := multipartUploadBody(t, "A Real Photo", "a description", testJPEG(t))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
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
}

func TestHandler_Delete_RemovesRowEvenIfStorageCleanupFails(t *testing.T) {
	mux, token := newFailingStorageTestServer(t)
	body, contentType := multipartUploadBody(t, "Will Be Deleted", "", testJPEG(t))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/photos", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
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
	delReq.Header.Set("Authorization", "Bearer "+token)
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
	mux, token := newTestServer(t)
	unknownID := "00000000-0000-0000-0000-000000000000"

	newTitle := "won't apply"
	patchBody, _ := json.Marshal(PhotoUpdate{Title: &newTitle})
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/admin/photos/"+unknownID, bytes.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Authorization", "Bearer "+token)
	patchRec := httptest.NewRecorder()
	mux.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusNotFound {
		t.Errorf("PATCH unknown id status = %d, want 404", patchRec.Code)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/photos/"+unknownID, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown id status = %d, want 404", delRec.Code)
	}
}
