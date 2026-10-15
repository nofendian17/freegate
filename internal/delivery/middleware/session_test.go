package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const sessionTestToken = "0123456789abcdef0123456789abcdef"

func mustIssueSession(t *testing.T, token string) string {
	t.Helper()
	v, err := IssueAdminSession(token)
	if err != nil {
		t.Fatalf("IssueAdminSession: %v", err)
	}
	return v
}

func TestAdminSession_IssueAndVerify(t *testing.T) {
	v := mustIssueSession(t, sessionTestToken)
	if !VerifyAdminSession(sessionTestToken, v) {
		t.Fatal("fresh session must verify")
	}
	if VerifyAdminSession("another-token-0123456789abcdef", v) {
		t.Fatal("session must not verify under a different token")
	}
	if VerifyAdminSession(sessionTestToken, "") || VerifyAdminSession("", v) {
		t.Fatal("empty token or cookie value must never verify")
	}
}

func TestAdminSession_DistinctValuePerLogin(t *testing.T) {
	a := mustIssueSession(t, sessionTestToken)
	b := mustIssueSession(t, sessionTestToken)
	if a == b {
		t.Fatal("two logins produced the same cookie value")
	}
}

func TestAdminSession_ExpiredRejected(t *testing.T) {
	exp := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	payload := "00112233445566778899aabbccddeeff." + exp
	expired := payload + "." + sessionMAC(sessionTestToken, payload)
	if VerifyAdminSession(sessionTestToken, expired) {
		t.Fatal("expired session must be rejected")
	}
}

func TestAdminSession_TamperedRejected(t *testing.T) {
	v := mustIssueSession(t, sessionTestToken)

	// A flipped signature byte must not verify.
	last := v[len(v)-1]
	repl := byte('0')
	if last == '0' {
		repl = '1'
	}
	if VerifyAdminSession(sessionTestToken, v[:len(v)-1]+string(repl)) {
		t.Fatal("tampered signature must be rejected")
	}

	// Extending the expiry without re-signing must not verify either.
	parts := strings.Split(v, ".")
	future := strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	bumped := parts[0] + "." + future + "." + parts[2]
	if VerifyAdminSession(sessionTestToken, bumped) {
		t.Fatal("expiry edited under a stale signature must be rejected")
	}
}

func TestAdminAuth_ValidSessionCookiePasses(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	m := AdminAuth(sessionTestToken, NewAuthThrottle())

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookie, Value: mustIssueSession(t, sessionTestToken)})
	rec := httptest.NewRecorder()
	m(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid session cookie: status = %d, want 200", rec.Code)
	}
}

func TestAdminAuth_ThrottlesFailedHeaderAttempts(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	m := AdminAuth(sessionTestToken, NewAuthThrottle())

	// A wrong X-Admin-Token counts as a failed attempt and redirects to
	// the login page.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Admin-Token", "wrong")
	rec := httptest.NewRecorder()
	m(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("wrong header: status = %d, want 302", rec.Code)
	}

	// The immediate retry is refused with 429 before the token comparison —
	// even the correct token — so header guessing is rate-limited like the
	// login form.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("X-Admin-Token", sessionTestToken)
	rec2 := httptest.NewRecorder()
	m(h).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("retry during backoff: status = %d, want 429", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Fatal("429 must carry Retry-After")
	}
}

func TestAdminAuth_InvalidCookieCountsAsFailure(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	m := AdminAuth(sessionTestToken, NewAuthThrottle())

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookie, Value: "junk"})
	rec := httptest.NewRecorder()
	m(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("junk cookie: status = %d, want 302", rec.Code)
	}

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("X-Admin-Token", sessionTestToken)
	rec2 := httptest.NewRecorder()
	m(h).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("retry during backoff: status = %d, want 429", rec2.Code)
	}
}

func TestAdminAuth_NoCredentialIsNotThrottled(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	m := AdminAuth(sessionTestToken, NewAuthThrottle())

	// Browsing without any credential is a normal unauthenticated visit,
	// not a failed guess: it redirects to /login and never engages the
	// throttle, no matter how often it repeats.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		m(h).ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("visit %d: status = %d, want 302", i+1, rec.Code)
		}
		if ra := rec.Header().Get("Retry-After"); ra != "" {
			t.Fatalf("visit %d: unauthenticated visit must not be throttled (Retry-After %s)", i+1, ra)
		}
	}
}
