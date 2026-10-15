package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// AdminSessionCookie names the dashboard login cookie set by POST /login.
const AdminSessionCookie = "fg_admin"

// AdminSessionTTL bounds how long a login session stays valid. It is a hard
// ceiling, not a sliding window: expiry is baked into the signed value.
const AdminSessionTTL = 12 * time.Hour

// IssueAdminSession returns a fresh session cookie value for adminToken: a
// random nonce plus an expiry timestamp, both signed with the admin token.
// Every login gets a distinct value, and a leaked cookie stops working after
// AdminSessionTTL — unlike the previous static HMAC, which was derived from
// the token alone, identical for every login, and valid forever.
func IssueAdminSession(adminToken string) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := hex.EncodeToString(nonce) + "." + strconv.FormatInt(time.Now().Add(AdminSessionTTL).Unix(), 10)
	return payload + "." + sessionMAC(adminToken, payload), nil
}

// VerifyAdminSession reports whether value is an unexpired session cookie
// signed for adminToken. An empty adminToken never verifies, so a
// misconfigured server fails closed instead of accepting a well-known value.
func VerifyAdminSession(adminToken, value string) bool {
	if adminToken == "" {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	want := sessionMAC(adminToken, parts[0]+"."+parts[1])
	return subtle.ConstantTimeCompare([]byte(parts[2]), []byte(want)) == 1
}

func sessionMAC(token, payload string) string {
	h := hmac.New(sha256.New, []byte(token))
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}
