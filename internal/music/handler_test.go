package music

import (
	"bytes"
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
// test can create a track/album normally and then exercise what happens
// when storage cleanup fails on delete, without the setup step itself
// failing.
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

// testMP3 returns bytes that pass isMP3's ID3v2-header check. Not a decodable
// stream of MPEG frames — trackDuration is built to tolerate that (returns
// nil rather than failing the upload), so tests exercise that same path real
// uploads hit for a file with a valid header but a malformed tag/frames.
func testMP3() []byte {
	return append([]byte("ID3"), bytes.Repeat([]byte{0}, 29)...)
}

func multipartTrackUploadBody(t *testing.T, fields map[string]string, fileBytes []byte) (*bytes.Buffer, string) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := w.CreateFormFile("file", "test.mp3")
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

func multipartAlbumBody(t *testing.T, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf, w.FormDataContentType()
}

// testJPEG returns a real, decodable JPEG — unlike testMP3, imageproc.Process
// actually decodes cover uploads, so a magic-byte-only stub won't do here.
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

func multipartCoverBody(t *testing.T, fileBytes []byte) (*bytes.Buffer, string) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	fw, err := w.CreateFormFile("cover", "cover.jpg")
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

func TestHandler_PublicRoute(t *testing.T) {
	mux := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/music", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/music status = %d, want 200", rec.Code)
	}
}

func TestHandler_CreateTrack_RejectsNonMP3(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartTrackUploadBody(t, map[string]string{"title": "Bad Upload"}, []byte("this is not an mp3 at all"))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/tracks", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestHandler_CreateTrack_RejectsOversizedBody(t *testing.T) {
	mux := newTestServer(t)
	oversized := bytes.Repeat([]byte{0}, 106<<20) // over the 105 MiB cap
	body, contentType := multipartTrackUploadBody(t, map[string]string{"title": "Too Big"}, oversized)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/tracks", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestHandler_CreateTrack_Success(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartTrackUploadBody(t, map[string]string{
		"title":       "A Real Track",
		"description": "a description",
	}, testMP3())

	req := httptest.NewRequest(http.MethodPost, "/api/admin/tracks", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}

	var got Track
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Title != "A Real Track" {
		t.Errorf("Title = %q, want %q", got.Title, "A Real Track")
	}
	if got.AudioURL == "" {
		t.Errorf("expected composed AudioURL, got empty string")
	}
	if got.AlbumID != nil {
		t.Errorf("AlbumID = %v, want nil (single, no album_id field sent)", *got.AlbumID)
	}
}

func TestHandler_CreateAlbum_Success(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartAlbumBody(t, map[string]string{
		"title":       "A Real Album",
		"description": "a description",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/albums", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}

	var got Album
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Title != "A Real Album" {
		t.Errorf("Title = %q, want %q", got.Title, "A Real Album")
	}
}

func TestHandler_CreateAlbum_RejectsInvalidMetadataJSON(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartAlbumBody(t, map[string]string{
		"title":    "Bad Metadata Album",
		"metadata": "not valid json",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/albums", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_ListMusic_GroupsTrackUnderItsAlbum(t *testing.T) {
	mux := newTestServer(t)

	albumBody, albumCT := multipartAlbumBody(t, map[string]string{"title": "Grouping Album"})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/albums", albumBody)
	req.Header.Set("Content-Type", albumCT)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: create album status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	var album Album
	if err := json.Unmarshal(rec.Body.Bytes(), &album); err != nil {
		t.Fatalf("decoding album response: %v", err)
	}

	trackBody, trackCT := multipartTrackUploadBody(t, map[string]string{
		"title":    "Grouped Track",
		"album_id": album.ID,
	}, testMP3())
	req = httptest.NewRequest(http.MethodPost, "/api/admin/tracks", trackBody)
	req.Header.Set("Content-Type", trackCT)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: create track status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/music", nil)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/music status = %d, want 200", listRec.Code)
	}

	var payload MusicPayload
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding music payload: %v", err)
	}
	var found *Album
	for _, a := range payload.Albums {
		if a.ID == album.ID {
			found = a
		}
	}
	if found == nil {
		t.Fatal("created album not present in GET /api/music")
	}
	if len(found.Tracks) != 1 || found.Tracks[0].Title != "Grouped Track" {
		t.Errorf("album.Tracks = %+v, want one track titled %q", found.Tracks, "Grouped Track")
	}
	for _, s := range payload.Singles {
		if s.Title == "Grouped Track" {
			t.Error("grouped track incorrectly also appears in singles")
		}
	}
}

func TestHandler_DeleteTrack_RemovesRowEvenIfStorageCleanupFails(t *testing.T) {
	mux := newFailingStorageTestServer(t)
	body, contentType := multipartTrackUploadBody(t, map[string]string{"title": "Will Be Deleted"}, testMP3())

	req := httptest.NewRequest(http.MethodPost, "/api/admin/tracks", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: create status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	var created Track
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/tracks/"+created.ID, nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 even though storage cleanup fails, body: %s", delRec.Code, delRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/music", nil)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	var payload MusicPayload
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding music payload: %v", err)
	}
	for _, s := range payload.Singles {
		if s.ID == created.ID {
			t.Error("track still present after delete despite failed storage cleanup")
		}
	}
}

func TestHandler_DeleteAlbum_DetachesTracksToSingles(t *testing.T) {
	mux := newTestServer(t)

	albumBody, albumCT := multipartAlbumBody(t, map[string]string{"title": "Album To Delete"})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/albums", albumBody)
	req.Header.Set("Content-Type", albumCT)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var album Album
	if err := json.Unmarshal(rec.Body.Bytes(), &album); err != nil {
		t.Fatalf("decoding album response: %v", err)
	}

	trackBody, trackCT := multipartTrackUploadBody(t, map[string]string{
		"title":    "Track To Detach",
		"album_id": album.ID,
	}, testMP3())
	req = httptest.NewRequest(http.MethodPost, "/api/admin/tracks", trackBody)
	req.Header.Set("Content-Type", trackCT)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var track Track
	if err := json.Unmarshal(rec.Body.Bytes(), &track); err != nil {
		t.Fatalf("decoding track response: %v", err)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/albums/"+album.ID, nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete album status = %d, want 204, body: %s", delRec.Code, delRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/music", nil)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	var payload MusicPayload
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding music payload: %v", err)
	}
	var found *Track
	for _, s := range payload.Singles {
		if s.ID == track.ID {
			found = s
		}
	}
	if found == nil {
		t.Fatal("track not found among singles after its album was deleted")
	}
}

func TestHandler_UnknownID_Returns404NotFor500(t *testing.T) {
	mux := newTestServer(t)
	unknownID := "00000000-0000-0000-0000-000000000000"

	newTitle := "won't apply"
	patchBody, _ := json.Marshal(TrackUpdate{Title: &newTitle})
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/admin/tracks/"+unknownID, bytes.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	mux.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusNotFound {
		t.Errorf("PATCH unknown track id status = %d, want 404", patchRec.Code)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/tracks/"+unknownID, nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown track id status = %d, want 404", delRec.Code)
	}

	albumPatchBody, _ := json.Marshal(AlbumUpdate{Title: &newTitle})
	albumPatchReq := httptest.NewRequest(http.MethodPatch, "/api/admin/albums/"+unknownID, bytes.NewReader(albumPatchBody))
	albumPatchReq.Header.Set("Content-Type", "application/json")
	albumPatchRec := httptest.NewRecorder()
	mux.ServeHTTP(albumPatchRec, albumPatchReq)
	if albumPatchRec.Code != http.StatusNotFound {
		t.Errorf("PATCH unknown album id status = %d, want 404", albumPatchRec.Code)
	}

	albumDelReq := httptest.NewRequest(http.MethodDelete, "/api/admin/albums/"+unknownID, nil)
	albumDelRec := httptest.NewRecorder()
	mux.ServeHTTP(albumDelRec, albumDelReq)
	if albumDelRec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown album id status = %d, want 404", albumDelRec.Code)
	}
}

// createAlbumWithCover builds the create request directly (rather than via
// multipartAlbumBody, which only handles text fields) so it includes a real
// cover file.
func createAlbumWithCover(t *testing.T, mux *http.ServeMux) Album {
	t.Helper()
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	_ = w.WriteField("title", "Has A Cover")
	fw, err := w.CreateFormFile("cover", "cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(testJPEG(t)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/albums", buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: create album with cover status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	var a Album
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if a.CoverURL == nil {
		t.Fatal("setup: expected the created album to have a cover")
	}
	return a
}

// createTrackWithCover is createAlbumWithCover's track equivalent.
func createTrackWithCover(t *testing.T, mux *http.ServeMux) Track {
	t.Helper()
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	_ = w.WriteField("title", "Has A Cover")
	audioFw, err := w.CreateFormFile("file", "test.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audioFw.Write(testMP3()); err != nil {
		t.Fatal(err)
	}
	coverFw, err := w.CreateFormFile("cover", "cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coverFw.Write(testJPEG(t)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/tracks", buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: create track with cover status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	var tr Track
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if tr.CoverURL == nil {
		t.Fatal("setup: expected the created track to have a cover")
	}
	return tr
}

func TestHandler_UpdateAlbumCover_AddsCoverToAlbumThatHadNone(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartAlbumBody(t, map[string]string{"title": "No Cover Yet"})
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/albums", body)
	createReq.Header.Set("Content-Type", contentType)
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	var created Album
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if created.CoverURL != nil {
		t.Fatalf("setup: expected no cover, got %v", created.CoverURL)
	}

	coverBody, coverContentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/albums/"+created.ID+"/cover", coverBody)
	req.Header.Set("Content-Type", coverContentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var updated Album
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.CoverURL == nil || *updated.CoverURL == "" {
		t.Error("expected a cover URL after uploading a cover, got none")
	}
}

func TestHandler_UpdateAlbumCover_ReplacesExistingCoverAndCleansUpOld(t *testing.T) {
	mux := newFailingStorageTestServer(t)
	created := createAlbumWithCover(t, mux)
	firstCoverURL := *created.CoverURL

	body, contentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/albums/"+created.ID+"/cover", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// newFailingStorageTestServer fails DELETE only — the replace must still
	// succeed even though best-effort cleanup of the old cover fails.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var updated Album
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.CoverURL == nil || *updated.CoverURL == firstCoverURL {
		t.Errorf("CoverURL = %v, want a new URL different from the original %q", updated.CoverURL, firstCoverURL)
	}
}

func TestHandler_UpdateAlbumCover_RejectsNonImage(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartAlbumBody(t, map[string]string{"title": "Bad Cover Target"})
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/albums", body)
	createReq.Header.Set("Content-Type", contentType)
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	var created Album
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}

	coverBody, coverContentType := multipartCoverBody(t, []byte("not an image"))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/albums/"+created.ID+"/cover", coverBody)
	req.Header.Set("Content-Type", coverContentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestHandler_UpdateAlbumCover_UnknownIDReturns404(t *testing.T) {
	mux := newTestServer(t)
	unknownID := "00000000-0000-0000-0000-000000000000"

	body, contentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/albums/"+unknownID+"/cover", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_UpdateTrackCover_AddsCoverToTrackThatHadNone(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartTrackUploadBody(t, map[string]string{"title": "No Cover Yet"}, testMP3())
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/tracks", body)
	createReq.Header.Set("Content-Type", contentType)
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	var created Track
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if created.CoverURL != nil {
		t.Fatalf("setup: expected no cover, got %v", created.CoverURL)
	}

	coverBody, coverContentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/tracks/"+created.ID+"/cover", coverBody)
	req.Header.Set("Content-Type", coverContentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var updated Track
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.CoverURL == nil || *updated.CoverURL == "" {
		t.Error("expected a cover URL after uploading a cover, got none")
	}
}

func TestHandler_UpdateTrackCover_ReplacesExistingCoverAndCleansUpOld(t *testing.T) {
	mux := newFailingStorageTestServer(t)
	created := createTrackWithCover(t, mux)
	firstCoverURL := *created.CoverURL

	body, contentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/tracks/"+created.ID+"/cover", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var updated Track
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.CoverURL == nil || *updated.CoverURL == firstCoverURL {
		t.Errorf("CoverURL = %v, want a new URL different from the original %q", updated.CoverURL, firstCoverURL)
	}
}

func TestHandler_UpdateTrackCover_RejectsNonImage(t *testing.T) {
	mux := newTestServer(t)
	body, contentType := multipartTrackUploadBody(t, map[string]string{"title": "Bad Cover Target"}, testMP3())
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/tracks", body)
	createReq.Header.Set("Content-Type", contentType)
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	var created Track
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}

	coverBody, coverContentType := multipartCoverBody(t, []byte("not an image"))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/tracks/"+created.ID+"/cover", coverBody)
	req.Header.Set("Content-Type", coverContentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestHandler_UpdateTrackCover_UnknownIDReturns404(t *testing.T) {
	mux := newTestServer(t)
	unknownID := "00000000-0000-0000-0000-000000000000"

	body, contentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/tracks/"+unknownID+"/cover", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
