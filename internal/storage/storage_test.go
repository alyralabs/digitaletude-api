package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSecretKey = "test-secret-key"

func TestPublicURL(t *testing.T) {
	c := New("https://example.supabase.co", testSecretKey)
	got := c.PublicURL("photography", "originals/abc.jpg")
	want := "https://example.supabase.co/storage/v1/object/public/photography/originals/abc.jpg"
	if got != want {
		t.Errorf("PublicURL() = %q, want %q", got, want)
	}
}

func TestUpload_SendsHeadersAndSucceeds(t *testing.T) {
	var gotReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, testSecretKey)
	err := c.Upload(context.Background(), "photography", "originals/abc.jpg", "image/jpeg", strings.NewReader("bytes"))
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if gotReq == nil {
		t.Fatal("mock server never received a request")
	}
	if gotReq.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", gotReq.Method)
	}
	if want := "/storage/v1/object/photography/originals/abc.jpg"; gotReq.URL.Path != want {
		t.Errorf("path = %s, want %s", gotReq.URL.Path, want)
	}
	if got := gotReq.Header.Get("apikey"); got != testSecretKey {
		t.Errorf("apikey header = %q, want %q", got, testSecretKey)
	}
	if got := gotReq.Header.Get("Authorization"); got != "Bearer "+testSecretKey {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer "+testSecretKey)
	}
	if got := gotReq.Header.Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type header = %q, want image/jpeg", got)
	}
	wantCacheControl := fmt.Sprintf("max-age=%d", CacheOneYear)
	if got := gotReq.Header.Get("Cache-Control"); got != wantCacheControl {
		t.Errorf("Cache-Control header = %q, want %q", got, wantCacheControl)
	}
}

func TestUpload_ErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := New(srv.URL, testSecretKey)
	err := c.Upload(context.Background(), "photography", "originals/abc.jpg", "image/jpeg", strings.NewReader("bytes"))
	if err == nil {
		t.Fatal("Upload() succeeded, want an error on a 500 response")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want it to mention the status code and body", err.Error())
	}
}

func TestDelete_SendsHeadersAndSucceeds(t *testing.T) {
	var gotReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, testSecretKey)
	err := c.Delete(context.Background(), "photography", "originals/abc.jpg")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if gotReq.Method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotReq.Method)
	}
	if want := "/storage/v1/object/photography/originals/abc.jpg"; gotReq.URL.Path != want {
		t.Errorf("path = %s, want %s", gotReq.URL.Path, want)
	}
	if got := gotReq.Header.Get("apikey"); got != testSecretKey {
		t.Errorf("apikey header = %q, want %q", got, testSecretKey)
	}
	if got := gotReq.Header.Get("Authorization"); got != "Bearer "+testSecretKey {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer "+testSecretKey)
	}
}

func TestDelete_ErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	c := New(srv.URL, testSecretKey)
	err := c.Delete(context.Background(), "photography", "originals/abc.jpg")
	if err == nil {
		t.Fatal("Delete() succeeded, want an error on a 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %q, want it to mention the status code", err.Error())
	}
}
