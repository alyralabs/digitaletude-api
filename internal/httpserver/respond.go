package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorBody is the single JSON error convention for the whole API:
// {"error": {"code": "...", "message": "..."}}
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encoding response", "error", err)
	}
}

// Err writes the error convention. message must be safe for public eyes —
// never pass raw Go error strings through here.
func Err(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// Internal logs the real error and hides it behind a generic 500.
func Internal(w http.ResponseWriter, err error) {
	slog.Error("internal error", "error", err)
	Err(w, http.StatusInternalServerError, "internal", "something went wrong")
}
