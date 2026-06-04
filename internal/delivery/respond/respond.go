package respond

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Error represents an OpenAI-compatible error response.
type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ErrorResponse is the standard error response body.
type ErrorResponse struct {
	Error Error `json:"error"`
}

// JSONError writes a JSON error response with the given status code.
func JSONError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := ErrorResponse{
		Error: Error{Type: errType, Message: msg},
	}
	json.NewEncoder(w).Encode(resp)
}

// JSON writes a JSON response with the given status code and body.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Ready writes a health check response.
func Ready(w http.ResponseWriter, ready bool) {
	w.Header().Set("Content-Type", "application/json")
	if ready {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprint(w, `{"status":"not ready"}`)
}
