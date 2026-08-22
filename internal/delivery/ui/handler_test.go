package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// NewTestRecorder mirrors the brief's helper name.
func NewTestRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// NewTestHandlerWithAdminToken is the brief's helper signature. It builds a
// Handler with the given admin token using the same template/static FS as
// newTestHandler. The second arg (recorder) is unused but kept for compat.
func NewTestHandlerWithAdminToken(adminToken string, _ *httptest.ResponseRecorder) *Handler {
	tpl, err := LoadTemplates(webTemplatesFS(&testing.T{}))
	if err != nil {
		panic(err)
	}
	return &Handler{
		data:       &fakeData{},
		vpn:        &fakeVPN{},
		templates:  tpl,
		staticFS:   webStaticFS(&testing.T{}),
		adminToken: adminToken,
	}
}

// newTestHandlerWithToken is the idiomatic test helper that reuses newTestHandler.
func newTestHandlerWithToken(t *testing.T, adminToken string) *Handler {
	t.Helper()
	h := newTestHandler(t)
	h.adminToken = adminToken
	return h
}

func TestLogin_Success_SetsCookie(t *testing.T) {
	rec := NewTestRecorder()
	h := newTestHandlerWithToken(t, "0123456789abcdef0123456789abcdef")
	form := url.Values{"admin_token": {"0123456789abcdef0123456789abcdef"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Login(w, req)
	if w.Code != 302 {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != "fg_admin" {
		t.Fatalf("cookie not set: %+v", cookies)
	}
	if cookies[0].Value != hmacForToken("0123456789abcdef0123456789abcdef") {
		t.Fatalf("cookie value mismatch: got %q want %q", cookies[0].Value, hmacForToken("0123456789abcdef0123456789abcdef"))
	}
	if !cookies[0].HttpOnly {
		t.Fatalf("cookie HttpOnly not set")
	}
	if cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie SameSite expected Lax, got %v", cookies[0].SameSite)
	}
	if cookies[0].Path != "/" {
		t.Fatalf("cookie Path expected /, got %q", cookies[0].Path)
	}
	_ = rec
	_ = NewTestHandlerWithAdminToken
}

func TestLogin_Failure_InvalidToken(t *testing.T) {
	h := newTestHandlerWithToken(t, "0123456789abcdef0123456789abcdef")
	form := url.Values{"admin_token": {"wrong"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Login(w, req)
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatalf("should not set cookie on failure")
	}
	body := w.Body.String()
	if !strings.Contains(body, "invalid token") {
		t.Fatalf("expected error message in body, got %q", body)
	}
}

func TestLogin_Success_WithNextParam(t *testing.T) {
	h := newTestHandlerWithToken(t, "0123456789abcdef0123456789abcdef")
	form := url.Values{"admin_token": {"0123456789abcdef0123456789abcdef"}, "next": {"/dashboard"}}
	req := httptest.NewRequest("POST", "/login?next=/dashboard", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Login(w, req)
	if w.Code != 302 {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/dashboard" {
		t.Fatalf("expected redirect to /dashboard, got %q", loc)
	}
}

func TestLogin_Success_SecureCookie(t *testing.T) {
	h := newTestHandlerWithToken(t, "0123456789abcdef0123456789abcdef")
	form := url.Values{"admin_token": {"0123456789abcdef0123456789abcdef"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.Login(w, req)
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].Secure {
		t.Fatalf("expected Secure cookie when X-Forwarded-Proto https, got %+v", cookies)
	}
}

func TestLoginPage_Renders(t *testing.T) {
	h := newTestHandlerWithToken(t, "0123456789abcdef0123456789abcdef")
	req := httptest.NewRequest("GET", "/login", nil)
	w := httptest.NewRecorder()
	h.LoginPage(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"admin_token", "login", "freegate", "TerminalUI", `type="password"`} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) && !strings.Contains(body, want) {
			t.Errorf("login page missing %q", want)
		}
	}
	// next param should be reflected in form action
	req2 := httptest.NewRequest("GET", "/login?next=/api/vpn/status", nil)
	w2 := httptest.NewRecorder()
	h.LoginPage(w2, req2)
	if !strings.Contains(w2.Body.String(), "/api/vpn/status") {
		t.Errorf("login page should reflect next param in form")
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	h := newTestHandlerWithToken(t, "0123456789abcdef0123456789abcdef")
	req := httptest.NewRequest("POST", "/logout", nil)
	w := httptest.NewRecorder()
	h.Logout(w, req)
	if w.Code != 302 {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != "fg_admin" {
		t.Fatalf("logout should clear fg_admin cookie, got %+v", cookies)
	}
	if cookies[0].MaxAge != -1 {
		t.Fatalf("expected MaxAge -1, got %d", cookies[0].MaxAge)
	}
	loc := w.Header().Get("Location")
	if loc != "/login" {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
}

func TestHmacForToken_Deterministic(t *testing.T) {
	a := hmacForToken("0123456789abcdef0123456789abcdef")
	b := hmacForToken("0123456789abcdef0123456789abcdef")
	if a != b {
		t.Fatalf("hmac not deterministic: %q vs %q", a, b)
	}
	if a == "0123456789abcdef0123456789abcdef" {
		t.Fatalf("hmac should not equal raw token")
	}
	if len(a) != 64 {
		t.Fatalf("hmac hex length expected 64, got %d (%q)", len(a), a)
	}
}
