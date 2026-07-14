package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCORS_NoAllowedOriginConfigured_IsNoOp(t *testing.T) {
	h := CORS("")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want unset when allowedOrigin is empty", got)
	}
}

func TestCORS_MatchingOrigin_SetsExactOriginNoWildcard(t *testing.T) {
	h := CORS("https://example.com")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the exact configured origin echoed back, never a wildcard", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want %q", got, "Origin")
	}
}

func TestCORS_NonMatchingOrigin_NoAllowHeaderButRequestStillProceeds(t *testing.T) {
	h := CORS("https://example.com")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want unset for a non-matching origin", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (same-origin/non-browser requests aren't blocked here, just left uncredentialed)", rec.Code)
	}
}

func TestCORS_Preflight_AnsweredBeforeReachingNextHandler(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	h := CORS("https://example.com")(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/admin/photos", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Error("preflight OPTIONS request reached the next handler, want it answered by CORS alone")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	for _, header := range []string{"Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Max-Age"} {
		if rec.Header().Get(header) == "" {
			t.Errorf("missing preflight response header %q", header)
		}
	}
}

func TestCORS_Preflight_NonMatchingOriginFallsThroughToNextHandler(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	h := CORS("https://example.com")(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/admin/photos", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Error("OPTIONS request from a non-matching origin should fall through rather than being answered as a preflight")
	}
}

func TestRecover_ConvertsPanicTo500WithoutLeakingDetails(t *testing.T) {
	panicking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("sensitive internal detail")
	})
	h := Recover(panicking)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Body.String(); strings.Contains(got, "sensitive internal detail") {
		t.Errorf("response body leaked the panic value: %q", got)
	}
}

func TestRecover_PassesThroughWhenNoPanic(t *testing.T) {
	h := Recover(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLog_CapturesActualStatusCodeWrittenByHandler(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	h := Log(next)

	req := httptest.NewRequest(http.MethodPost, "/api/photos", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (Log must not alter the response)", rec.Code)
	}
}
