package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"freegate/internal/delivery/middleware"
)

const providersTestAdminToken = "0123456789abcdef0123456789abcdef"

// TestProvidersPage_RequiresAdmin mirrors the brief's test, adapted to the
// repo's auth model: admin gating lives in middleware.AdminAuth at the
// server.go mount, not in Routes(). The test wraps Routes() the same way.
func TestProvidersPage_RequiresAdmin(t *testing.T) {
	h := newTestHandler(t)
	h.adminToken = providersTestAdminToken
	srv := middleware.AdminAuth(providersTestAdminToken)(h.Routes())
	req := httptest.NewRequest("GET", "/providers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("expected non-200 without admin (redirect or 401)")
	}
}

func TestProvidersPage_Renders(t *testing.T) {
	h := newTestHandler(t)
	h.adminToken = providersTestAdminToken
	srv := middleware.AdminAuth(providersTestAdminToken)(h.Routes())
	req := httptest.NewRequest("GET", "/providers", nil)
	req.AddCookie(&http.Cookie{Name: "fg_admin", Value: middleware.HmacForToken(providersTestAdminToken)})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"provider-table", "provider-modal", "/api/providers", "/api/combos", "freegate"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
