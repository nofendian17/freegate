package vpn

import "testing"

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
