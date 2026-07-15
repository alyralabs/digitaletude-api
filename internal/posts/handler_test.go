package posts

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
// test can create a post (with a cover) normally and then exercise what
// happens when storage cleanup of the old cover fails on replace, without
// the setup step itself failing.
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

func multipartPostBody(t *testing.T, fields map[string]string) (*bytes.Buffer, string) {
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

func createTestPost(t *testing.T, mux *http.ServeMux, fields map[string]string) Post {
	t.Helper()
	body, contentType := multipartPostBody(t, fields)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/posts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: create post status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	var p Post
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	return p
}

func TestHandler_PublicRoutes(t *testing.T) {
	mux := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/posts", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/posts status = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/posts/unknown-slug", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/posts/{unknown-slug} status = %d, want 404", rec.Code)
	}
}

func TestHandler_Create_Success(t *testing.T) {
	mux := newTestServer(t)
	created := createTestPost(t, mux, map[string]string{
		"title":           "A Real Post",
		"excerpt":         "a hand-written excerpt",
		"contentMarkdown": "# Heading\n\nsome body text.",
	})

	if created.Title != "A Real Post" {
		t.Errorf("Title = %q, want %q", created.Title, "A Real Post")
	}
	if created.Slug != "a-real-post" {
		t.Errorf("Slug = %q, want %q", created.Slug, "a-real-post")
	}
	if created.Status != "draft" {
		t.Errorf("Status = %q, want %q", created.Status, "draft")
	}
}

func TestHandler_ListPublic_ExcludesDraftsAndDerivesExcerpt(t *testing.T) {
	mux := newTestServer(t)
	draft := createTestPost(t, mux, map[string]string{
		"title":           "A Draft",
		"contentMarkdown": "draft body",
	})
	published := createTestPost(t, mux, map[string]string{
		"title":           "A Published Post",
		"contentMarkdown": "# Heading\n\nThis body has no explicit excerpt set.",
	})

	pubReq := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+published.ID+"/publish", nil)
	pubRec := httptest.NewRecorder()
	mux.ServeHTTP(pubRec, pubReq)
	if pubRec.Code != http.StatusOK {
		t.Fatalf("publish status = %d, want 200, body: %s", pubRec.Code, pubRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/posts", nil)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/posts status = %d, want 200", listRec.Code)
	}

	var summaries []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}

	var found map[string]any
	for _, s := range summaries {
		if s["slug"] == draft.Slug {
			t.Error("draft post appeared in public list")
		}
		if s["slug"] == published.Slug {
			found = s
		}
		if _, hasContent := s["contentMarkdown"]; hasContent {
			t.Error("public list summary leaked contentMarkdown")
		}
	}
	if found == nil {
		t.Fatal("published post not found in public list")
	}
	excerpt, _ := found["excerpt"].(string)
	if excerpt == "" {
		t.Error("expected a derived excerpt for a post with no explicit excerpt, got empty string")
	}
	if bytes.Contains([]byte(excerpt), []byte("#")) {
		t.Errorf("excerpt = %q, want markdown heading syntax stripped", excerpt)
	}
}

func TestHandler_GetBySlug_HidesDraftsAsNotFound(t *testing.T) {
	mux := newTestServer(t)
	draft := createTestPost(t, mux, map[string]string{
		"title":           "Hidden Draft",
		"contentMarkdown": "body",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/posts/"+draft.Slug, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/posts/{draft-slug} status = %d, want 404", rec.Code)
	}

	pubReq := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+draft.ID+"/publish", nil)
	pubRec := httptest.NewRecorder()
	mux.ServeHTTP(pubRec, pubReq)
	if pubRec.Code != http.StatusOK {
		t.Fatalf("publish status = %d, want 200", pubRec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/posts/"+draft.Slug, nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/posts/{now-published-slug} status = %d, want 200", rec.Code)
	}
}

func TestHandler_ListAdmin_IncludesDraftsAndFullFields(t *testing.T) {
	mux := newTestServer(t)
	createTestPost(t, mux, map[string]string{
		"title":           "Admin Visible Draft",
		"contentMarkdown": "secret draft body",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/posts", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/posts status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	var list []Post
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding admin list: %v", err)
	}
	var found *Post
	for i, p := range list {
		if p.Title == "Admin Visible Draft" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("draft post not present in admin list")
	}
	if found.ContentMarkdown != "secret draft body" {
		t.Errorf("ContentMarkdown = %q, want full body present in admin list", found.ContentMarkdown)
	}
}

func TestHandler_PublishUnpublish_RoundTrip(t *testing.T) {
	mux := newTestServer(t)
	created := createTestPost(t, mux, map[string]string{
		"title":           "Publish Cycle Post",
		"contentMarkdown": "body",
	})

	pubReq := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+created.ID+"/publish", nil)
	pubRec := httptest.NewRecorder()
	mux.ServeHTTP(pubRec, pubReq)
	if pubRec.Code != http.StatusOK {
		t.Fatalf("publish status = %d, want 200, body: %s", pubRec.Code, pubRec.Body.String())
	}
	var published Post
	if err := json.Unmarshal(pubRec.Body.Bytes(), &published); err != nil {
		t.Fatalf("decoding publish response: %v", err)
	}
	if published.Status != "published" {
		t.Errorf("Status = %q, want %q", published.Status, "published")
	}

	unpubReq := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+created.ID+"/unpublish", nil)
	unpubRec := httptest.NewRecorder()
	mux.ServeHTTP(unpubRec, unpubReq)
	if unpubRec.Code != http.StatusOK {
		t.Fatalf("unpublish status = %d, want 200, body: %s", unpubRec.Code, unpubRec.Body.String())
	}
	var unpublished Post
	if err := json.Unmarshal(unpubRec.Body.Bytes(), &unpublished); err != nil {
		t.Fatalf("decoding unpublish response: %v", err)
	}
	if unpublished.Status != "draft" {
		t.Errorf("Status = %q after unpublish, want %q", unpublished.Status, "draft")
	}
}

func TestHandler_Update_SlugFrozenOncePublished(t *testing.T) {
	mux := newTestServer(t)
	created := createTestPost(t, mux, map[string]string{
		"title":           "Freeze Me Via Handler",
		"contentMarkdown": "body",
	})

	pubReq := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+created.ID+"/publish", nil)
	pubRec := httptest.NewRecorder()
	mux.ServeHTTP(pubRec, pubReq)
	if pubRec.Code != http.StatusOK {
		t.Fatalf("publish status = %d, want 200", pubRec.Code)
	}

	newSlug := "attempted-new-slug"
	patchBody, _ := json.Marshal(PostUpdate{Slug: &newSlug})
	putReq := httptest.NewRequest(http.MethodPut, "/api/admin/posts/"+created.ID, bytes.NewReader(patchBody))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	mux.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200, body: %s", putRec.Code, putRec.Body.String())
	}
	var updated Post
	if err := json.Unmarshal(putRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding update response: %v", err)
	}
	if updated.Slug != created.Slug {
		t.Errorf("Slug = %q after publish, want unchanged %q", updated.Slug, created.Slug)
	}
}

func TestHandler_Delete_RemovesPost(t *testing.T) {
	mux := newTestServer(t)
	created := createTestPost(t, mux, map[string]string{
		"title":           "Will Be Deleted",
		"contentMarkdown": "body",
	})

	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/posts/"+created.ID, nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204, body: %s", delRec.Code, delRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/admin/posts/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Errorf("GET after delete status = %d, want 404", getRec.Code)
	}
}

func TestHandler_UnknownID_Returns404NotFor500(t *testing.T) {
	mux := newTestServer(t)
	unknownID := "00000000-0000-0000-0000-000000000000"

	getReq := httptest.NewRequest(http.MethodGet, "/api/admin/posts/"+unknownID, nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Errorf("GET unknown id status = %d, want 404", getRec.Code)
	}

	newTitle := "won't apply"
	putBody, _ := json.Marshal(PostUpdate{Title: &newTitle})
	putReq := httptest.NewRequest(http.MethodPut, "/api/admin/posts/"+unknownID, bytes.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	mux.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusNotFound {
		t.Errorf("PUT unknown id status = %d, want 404", putRec.Code)
	}

	pubReq := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+unknownID+"/publish", nil)
	pubRec := httptest.NewRecorder()
	mux.ServeHTTP(pubRec, pubReq)
	if pubRec.Code != http.StatusNotFound {
		t.Errorf("PATCH publish unknown id status = %d, want 404", pubRec.Code)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/posts/"+unknownID, nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown id status = %d, want 404", delRec.Code)
	}
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

func TestHandler_UpdateCover_AddsCoverToPostThatHadNone(t *testing.T) {
	mux := newTestServer(t)
	created := createTestPost(t, mux, map[string]string{
		"title":           "No Cover Yet",
		"contentMarkdown": "body",
	})
	if created.CoverURL != nil {
		t.Fatalf("setup: expected no cover, got %v", created.CoverURL)
	}

	body, contentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+created.ID+"/cover", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var updated Post
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.CoverURL == nil || *updated.CoverURL == "" {
		t.Error("expected a cover URL after uploading a cover, got none")
	}
}

func TestHandler_UpdateCover_ReplacesExistingCoverAndCleansUpOld(t *testing.T) {
	mux := newFailingStorageTestServer(t)

	// Build the create request directly so it includes a real cover file
	// (createTestPost/multipartPostBody only handle text fields).
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	_ = w.WriteField("title", "Has A Cover")
	_ = w.WriteField("contentMarkdown", "body")
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
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/posts", buf)
	createReq.Header.Set("Content-Type", w.FormDataContentType())
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("setup: create with cover status = %d, want 201, body: %s", createRec.Code, createRec.Body.String())
	}
	var withCover Post
	if err := json.Unmarshal(createRec.Body.Bytes(), &withCover); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if withCover.CoverURL == nil {
		t.Fatal("setup: expected the created post to have a cover")
	}
	firstCoverURL := *withCover.CoverURL

	body, contentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+withCover.ID+"/cover", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// newFailingStorageTestServer fails DELETE only — the replace must still
	// succeed (204/200) even though best-effort cleanup of the old cover
	// fails, same posture as delete-with-failed-cleanup elsewhere.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var updated Post
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.CoverURL == nil || *updated.CoverURL == firstCoverURL {
		t.Errorf("CoverURL = %v, want a new URL different from the original %q", updated.CoverURL, firstCoverURL)
	}
}

func TestHandler_UpdateCover_RejectsNonImage(t *testing.T) {
	mux := newTestServer(t)
	created := createTestPost(t, mux, map[string]string{
		"title":           "Bad Cover Target",
		"contentMarkdown": "body",
	})

	body, contentType := multipartCoverBody(t, []byte("not an image"))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+created.ID+"/cover", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestHandler_UpdateCover_UnknownIDReturns404(t *testing.T) {
	mux := newTestServer(t)
	unknownID := "00000000-0000-0000-0000-000000000000"

	body, contentType := multipartCoverBody(t, testJPEG(t))
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/posts/"+unknownID+"/cover", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
