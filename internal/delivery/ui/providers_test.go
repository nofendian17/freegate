package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"freegate/internal/delivery/middleware"
)

const providersTestAdminToken = "0123456789abcdef0123456789abcdef"

// adminSessionCookie builds a valid fg_admin cookie for the test admin token.
func adminSessionCookie(t *testing.T, token string) *http.Cookie {
	t.Helper()
	sess, err := middleware.IssueAdminSession(token)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: middleware.AdminSessionCookie, Value: sess}
}

// TestProvidersPage_RequiresAdmin mirrors the brief's test, adapted to the
// repo's auth model: admin gating lives in middleware.AdminAuth at the
// server.go mount, not in Routes(). The test wraps Routes() the same way.
func TestProvidersPage_RequiresAdmin(t *testing.T) {
	h := newTestHandler(t)
	h.adminToken = providersTestAdminToken
	srv := middleware.AdminAuth(providersTestAdminToken, nil)(h.Routes())
	req := httptest.NewRequest("GET", "/providers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("expected non-200 without admin (redirect or 401)")
	}
}

func TestProvidersPage_RendersComboSection(t *testing.T) {
	h := newTestHandler(t)
	h.adminToken = providersTestAdminToken
	srv := middleware.AdminAuth(providersTestAdminToken, nil)(h.Routes())
	req := httptest.NewRequest("GET", "/providers", nil)
	req.AddCookie(adminSessionCookie(t, providersTestAdminToken))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tiers") && !strings.Contains(w.Body.String(), "Tiers") {
		t.Fatal("combo tier UI missing")
	}
}

func TestProvidersPage_Renders(t *testing.T) {
	h := newTestHandler(t)
	h.adminToken = providersTestAdminToken
	srv := middleware.AdminAuth(providersTestAdminToken, nil)(h.Routes())
	req := httptest.NewRequest("GET", "/providers", nil)
	req.AddCookie(adminSessionCookie(t, providersTestAdminToken))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"provider-table", "provider-modal", "/api/providers", "/api/combos", "providers.js", "freegate"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestProvidersPage_RendersPoolSection(t *testing.T) {
	h := newTestHandler(t)
	h.adminToken = providersTestAdminToken
	srv := middleware.AdminAuth(providersTestAdminToken, nil)(h.Routes())
	req := httptest.NewRequest("GET", "/providers", nil)
	req.AddCookie(adminSessionCookie(t, providersTestAdminToken))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"pool-table", "/api/pools", "pool-deploy", "pool-modal", "vercel-deploy", "used once for deployment and not stored", "https://vercel.com/account/settings/tokens", "Get token"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
