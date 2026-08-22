package vpn

import (
	"context"
	"errors"
	"testing"
)

func TestLinuxProvider_PreflightMissingOpenVPN(t *testing.T) {
	orig := execLookPath
	execLookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { execLookPath = orig }()
	p := newLinuxProvider(ProviderConfig{Enabled: true, SocksAddr: "127.0.0.1:9050"})
	if err := p.Start(context.Background()); err != nil {
		t.Errorf("expected fallback nil error when openvpn missing, got %v", err)
	}
	if got := p.CurrentIP(); got != "direct" && got != "" {
		t.Errorf("expected direct or empty on fallback, got %s", got)
	}
}

func TestNewProvider_AutoReturnsDirectWhenDisabled(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p == nil {
		t.Fatal("expected provider")
	}
	if p.CurrentIP() != "direct" {
		t.Errorf("expected direct, got %s", p.CurrentIP())
	}
}
