package vpn

import (
	"context"
	"testing"
)

func TestNewProvider_AutoReturnsDirectWhenDisabled(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p == nil {
		t.Fatal("expected provider")
	}
	if got := p.CurrentIP(); got != "direct" {
		t.Errorf("expected direct, got %s", got)
	}
}

func TestNewProvider_ProviderDirect(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Enabled: true, Provider: "direct"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := p.(*directProvider); !ok {
		t.Fatalf("expected directProvider, got %T", p)
	}
}

func TestDirectProvider_Noops(t *testing.T) {
	d := &directProvider{}
	if err := d.Start(context.Background()); err != nil {
		t.Errorf("Start: %v", err)
	}
	if err := d.Rotate(); err != nil {
		t.Errorf("Rotate: %v", err)
	}
	if err := d.ConnectTo("x"); err != nil {
		t.Errorf("ConnectTo: %v", err)
	}
	servers, err := d.ListServers()
	if servers != nil || err != nil {
		t.Errorf("ListServers = %v, %v; want nil, nil", servers, err)
	}
	st, err := d.Status()
	if err != nil || st.Connected {
		t.Errorf("Status = %+v, %v; want zero, nil", st, err)
	}
	pr, err := d.Ping()
	if err != nil || !pr.Direct {
		t.Errorf("Ping = %+v, %v; want Direct=true", pr, err)
	}
	if d.InstallHint() != "" {
		t.Error("InstallHint should be empty")
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
