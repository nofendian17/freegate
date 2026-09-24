package registry

import (
	"errors"
	"testing"
)

func TestStore_PoolCRUD(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.CreatePool(t.Context(), ProxyPool{Name: "relay-1", ProxyURL: "https://relay-1.example.com", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected nonzero ID")
	}
	if _, err := s.CreatePool(t.Context(), ProxyPool{Name: "bad", ProxyURL: "not-a-url"}); err == nil {
		t.Fatal("expected validation error for bad proxy_url")
	}
	list, err := s.ListPools(t.Context())
	if err != nil || len(list) != 1 || list[0].ProxyURL != "https://relay-1.example.com" {
		t.Fatalf("list: %v %+v", err, list)
	}
	got, err := s.GetPool(t.Context(), p.ID)
	if err != nil || got.Name != "relay-1" {
		t.Fatalf("get: %v %+v", err, got)
	}
	upd, err := s.UpdatePool(t.Context(), p.ID, ProxyPool{Name: "relay-1", ProxyURL: "https://relay-2.example.com", NoProxy: "example.com", Enabled: false})
	if err != nil || upd.ProxyURL != "https://relay-2.example.com" || upd.NoProxy != "example.com" || upd.Enabled {
		t.Fatalf("update: %v %+v", err, upd)
	}
	if err := s.DeletePool(t.Context(), p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := s.ListPools(t.Context()); len(list) != 0 {
		t.Fatalf("expected empty after delete, got %+v", list)
	}
}

func TestUpdatePool_PreservesTestStatus(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	pool, err := s.CreatePool(t.Context(), ProxyPool{Name: "relay", ProxyURL: "https://relay.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPoolTest(t.Context(), pool.ID, false, "dial failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePool(t.Context(), pool.ID, ProxyPool{Name: "relay-new", ProxyURL: "https://relay-new.test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPool(t.Context(), pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TestStatus != "error" || got.LastError != "dial failed" {
		t.Fatalf("pool update overwrote test result: %+v", got)
	}
}

func TestStore_BuiltinProxy(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	b, err := s.GetBuiltinProxy(t.Context(), "opencode")
	if err != nil || b.Name != "opencode" || NormalizeProxyMode(b.ProxyMode) != ProxyModeGlobal || b.ProxyPoolID != nil {
		t.Fatalf("default must be global: %+v %v", b, err)
	}
	if _, err := s.SetBuiltinProxy(t.Context(), "nope", "direct", nil); err == nil {
		t.Fatal("expected unknown builtin to fail")
	}
	if _, err := s.SetBuiltinProxy(t.Context(), "opencode", "sideways", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown proxy mode error = %v, want ErrInvalidArgument", err)
	}
	if _, err := s.SetBuiltinProxy(t.Context(), "kilo", "pool", nil); err == nil {
		t.Fatal("expected pool mode without id to fail")
	}
	bad := uint(999999)
	if _, err := s.SetBuiltinProxy(t.Context(), "kilo", "pool", &bad); err == nil {
		t.Fatal("expected missing pool to fail")
	}
	pool, err := s.CreatePool(t.Context(), ProxyPool{Name: "edge-1", ProxyURL: "https://relay.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBuiltinProxy(t.Context(), "kilo", "pool", &pool.ID); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	b, err = s.GetBuiltinProxy(t.Context(), "kilo")
	if err != nil || b.ProxyMode != ProxyModePool || b.ProxyPoolID == nil || *b.ProxyPoolID != pool.ID {
		t.Fatalf("pin not stored: %+v %v", b, err)
	}
	if _, err := s.SetBuiltinProxy(t.Context(), "kilo", "direct", &pool.ID); err != nil {
		t.Fatalf("set direct: %v", err)
	}
	b, err = s.GetBuiltinProxy(t.Context(), "kilo")
	if err != nil || b.ProxyMode != ProxyModeDirect || b.ProxyPoolID != nil {
		t.Fatalf("direct must clear pin: %+v %v", b, err)
	}
	if _, err := s.SetBuiltinProxy(t.Context(), "llm7", "pool", &pool.ID); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	if err := s.UnpinPool(t.Context(), pool.ID); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	b, err = s.GetBuiltinProxy(t.Context(), "llm7")
	if err != nil || NormalizeProxyMode(b.ProxyMode) != ProxyModeGlobal || b.ProxyPoolID != nil {
		t.Fatalf("unpin must reset builtin to global: %+v %v", b, err)
	}
	list, err := s.ListBuiltinProxies(t.Context())
	if err != nil || len(list) != len(KnownBuiltins) {
		t.Fatalf("list builtin: %v %+v", err, list)
	}
}

// TestStore_DeletePoolResetsPins verifies pool deletion resets every pin
// on it (custom providers and builtins) in the same transaction: the pool
// is gone and no row still references it.
func TestStore_DeletePoolResetsPins(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pool, err := s.CreatePool(t.Context(), ProxyPool{Name: "edge-1", ProxyURL: "https://relay.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CreateProvider(t.Context(), Provider{Name: "pinned", BaseURL: "https://example.test/v1", APIKeys: []string{"k"}, Enabled: true, ProxyMode: "pool", ProxyPoolID: &pool.ID})
	if err != nil {
		t.Fatalf("create pinned: %v", err)
	}
	if _, err := s.SetBuiltinProxy(t.Context(), "kilo", "pool", &pool.ID); err != nil {
		t.Fatalf("set builtin pin: %v", err)
	}
	if err := s.DeletePool(t.Context(), pool.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetPool(t.Context(), pool.ID); err == nil {
		t.Fatal("pool must be gone")
	}
	raw, err := s.GetProviderRaw(t.Context(), pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.EffectiveProxyMode() != ProxyModeGlobal || raw.ProxyPoolID != nil {
		t.Fatalf("custom pin must reset to global: %+v", raw)
	}
	b, err := s.GetBuiltinProxy(t.Context(), "kilo")
	if err != nil || NormalizeProxyMode(b.ProxyMode) != ProxyModeGlobal || b.ProxyPoolID != nil {
		t.Fatalf("builtin pin must reset to global: %+v %v", b, err)
	}
}
