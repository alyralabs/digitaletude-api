package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSON_WritesStatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusCreated, map[string]string{"title": "hello"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if got["title"] != "hello" {
		t.Errorf("body = %v, want title=hello", got)
	}
}

func TestJSON_NilBodyWritesNoContent(t *testing.T) {
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusNoContent, nil)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty for a nil payload", rec.Body.String())
	}
}

func TestErr_WritesTheSharedErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	Err(rec, http.StatusNotFound, "not_found", "photo not found")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Error.Code != "not_found" || body.Error.Message != "photo not found" {
		t.Errorf("body = %+v, want code=not_found message=%q", body, "photo not found")
	}
}

func TestInternal_Hides500DetailBehindGenericMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	Internal(rec, errWithSensitiveDetail{})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Error.Code != "internal" {
		t.Errorf("Error.Code = %q, want %q", body.Error.Code, "internal")
	}
	if body.Error.Message != "something went wrong" {
		t.Errorf("Error.Message = %q, want the generic message, not the real error text", body.Error.Message)
	}
}

type errWithSensitiveDetail struct{}

func (errWithSensitiveDetail) Error() string { return "sensitive: password=hunter2" }
