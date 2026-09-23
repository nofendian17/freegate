package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRespondStoreError_HidesInternalDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	respondStoreError(recorder, errors.New("sqlite schema detail: providers.api_keys"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "providers.api_keys") {
		t.Fatalf("response exposed database details: %s", recorder.Body.String())
	}
}
