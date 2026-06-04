package tor

import (
	"testing"
	"time"
)

func TestNewController(t *testing.T) {
	c := NewController("127.0.0.1", 9051, "testpass", "127.0.0.1:9050")
	if c == nil {
		t.Fatal("expected non-nil Controller")
	}
	if c.host != "127.0.0.1" {
		t.Errorf("expected host '127.0.0.1', got %q", c.host)
	}
	if c.port != 9051 {
		t.Errorf("expected port 9051, got %d", c.port)
	}
	if c.pass != "testpass" {
		t.Errorf("expected pass 'testpass', got %q", c.pass)
	}
	if c.socks != "127.0.0.1:9050" {
		t.Errorf("expected socks '127.0.0.1:9050', got %q", c.socks)
	}
}

func TestController_NewIP_TooSoon(t *testing.T) {
	c := NewController("127.0.0.1", 9999, "", "127.0.0.1:9050")
	// Set lastIP to now so the minimum interval check triggers
	c.lastIP = time.Now()

	err := c.NewIP()
	if err != nil {
		t.Errorf("expected nil error when too soon, got %v", err)
	}
}

func TestController_Close(t *testing.T) {
	c := NewController("127.0.0.1", 9999, "", "127.0.0.1:9050")
	// Close should not panic
	c.Close()
}
