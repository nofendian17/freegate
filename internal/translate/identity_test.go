package translate

import (
	"context"
	"strings"
	"testing"
)

func TestOpenCodeSessionRE(t *testing.T) {
	for _, id := range []string{
		"ses_0afae3e4c001AmMPIe8RFqNeTF",
		"ses_f5051c1b3ffeIKElIsuBAg4ZSs",
	} {
		if !OpenCodeSessionRE.MatchString(id) {
			t.Errorf("expected canonical %q", id)
		}
	}
	for _, id := range []string{
		"",
		"ses_F5051C1B3FFEIKElIsuBAg4ZSs",
		"ses_short",
		"msg_0afae3e4c001AmMPIe8RFqNeTF",
		"claude:abc-123",
		"ses_0afae3e4c001AmMPIe8RFqNeTF!",
	} {
		if OpenCodeSessionRE.MatchString(id) {
			t.Errorf("expected non-canonical %q", id)
		}
	}
}

func TestTranslateSessionID(t *testing.T) {
	canonical := "ses_0afae3e4c001AmMPIe8RFqNeTF"
	if got := TranslateSessionID("  "+canonical+"  ", "cli"); got != canonical {
		t.Fatalf("verbatim passthrough broken, got %q", got)
	}
	// Vectors cross-checked against 9router's translateSessionId (node crypto).
	if got := TranslateSessionID("claude:abc-123", "cli"); got != "ses_6609835477911Oz2ay5KPkdfLH" {
		t.Fatalf("mapping mismatch, got %q", got)
	}
	if got := TranslateSessionID("", "desktop"); got != "ses_3f22435d39443hTnPAWYkPHhxD" {
		t.Fatalf("empty mapping mismatch, got %q", got)
	}
	a, b := TranslateSessionID("x", "cli"), TranslateSessionID("x", "cli")
	if a != b {
		t.Fatal("mapping not deterministic")
	}
	if TranslateSessionID("x", "cli") == TranslateSessionID("y", "cli") {
		t.Fatal("distinct inputs collided")
	}
	if got := TranslateSessionID("x", "cli"); !OpenCodeSessionRE.MatchString(got) {
		t.Fatalf("mapped id %q not canonical", got)
	}
}

func TestValidOpencodeVersion(t *testing.T) {
	for _, ua := range []string{
		"opencode/1.17.0",
		"opencode/1.18.31",
		"opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
		"opencode/2.0.0",
	} {
		if !ValidOpencodeVersion(ua) {
			t.Errorf("expected valid %q", ua)
		}
	}
	for _, ua := range []string{
		"",
		"opencode",
		"opencode/1.16.9",
		"opencode/0.9.0",
		"claude-code/1.0",
		"Go-http-client/1.1",
	} {
		if ValidOpencodeVersion(ua) {
			t.Errorf("expected invalid %q", ua)
		}
	}
}

func TestDownstreamIdentityContext(t *testing.T) {
	if got := DownstreamIdentityFrom(context.Background()); got != (DownstreamIdentity{}) {
		t.Fatalf("expected zero value, got %+v", got)
	}
	want := DownstreamIdentity{UserAgent: "opencode/1.18.31", Session: "ses_abc", Client: "cli"}
	ctx := WithDownstreamIdentity(context.Background(), want)
	if got := DownstreamIdentityFrom(ctx); got != want {
		t.Fatalf("round trip broken, got %+v", got)
	}
}

func TestClipIdentity(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := ClipIdentity(DownstreamIdentity{Session: "  ses_abc  ", RequestID: long})
	if got.Session != "ses_abc" {
		t.Fatalf("not trimmed: %q", got.Session)
	}
	if len(got.RequestID) != 256 {
		t.Fatalf("not capped: %d", len(got.RequestID))
	}
}
