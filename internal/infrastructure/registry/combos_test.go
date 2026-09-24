package registry

import (
	"testing"
)

func TestSaveCombo_UnknownCustom_Rejected(t *testing.T) {
	s, err := Open(t.Context(), "file:ghostcombo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.SaveCombo(t.Context(), RouteCombo{Name: "ghost", Tiers: []ComboTier{{Provider: "custom:ghost"}}}); err == nil {
		t.Fatal("expected error for unknown custom provider")
	}
	p, err := s.CreateProvider(t.Context(), Provider{Name: "off", BaseURL: "https://off.test/v1", APIKeys: []string{"k"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p.Enabled = false
	if _, err := s.UpdateProvider(t.Context(), p.ID, p); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := s.SaveCombo(t.Context(), RouteCombo{Name: "ghost2", Tiers: []ComboTier{{Provider: "custom:off"}}}); err == nil {
		t.Fatal("expected error for disabled custom provider")
	}
	if _, err := s.UpdateCombo(t.Context(), 1, RouteCombo{Name: "ghost3", Tiers: []ComboTier{{Provider: "custom:ghost"}}}); err == nil {
		t.Fatal("expected error for unknown custom provider on update")
	}
}

func TestSaveCombo_TrimsTierProviders(t *testing.T) {
	s, err := Open(t.Context(), "file:trimcombo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(t.Context(), RouteCombo{Name: "trim", Tiers: []ComboTier{{Provider: " opencode "}}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if c.Tiers[0].Provider != "opencode" {
		t.Fatalf("provider not trimmed: %q", c.Tiers[0].Provider)
	}
}

func TestCombo_Update_PreservesID(t *testing.T) {
	s, err := Open(t.Context(), "file:updatecombo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(t.Context(), RouteCombo{Name: "upd", Tiers: []ComboTier{{Provider: "opencode"}}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	u, err := s.UpdateCombo(t.Context(), c.ID, RouteCombo{Name: "upd", Tiers: []ComboTier{{Provider: "opencode"}, {Provider: "kilo"}}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if u.ID != c.ID {
		t.Fatalf("expected same ID %d, got %d", c.ID, u.ID)
	}
	if len(u.Tiers) != 2 {
		t.Fatalf("expected 2 tiers, got %+v", u.Tiers)
	}
	list, err := s.ListCombos(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, x := range list {
		if x.Name == "upd" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected 1 row named upd, got %d", n)
	}
}

func TestCombo_Tiers_CRUD(t *testing.T) {
	s, err := Open(t.Context(), "file:tiercrud?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(t.Context(), RouteCombo{Name: "hemat", Tiers: []ComboTier{{Provider: "opencode"}, {Provider: "kilo", Model: "kilo-auto"}}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(c.Tiers) != 2 || c.Tiers[0].Provider != "opencode" {
		t.Fatalf("bad tiers: %+v", c.Tiers)
	}
	list, err := s.ListCombos(t.Context())
	if err != nil || len(list) != 1 || len(list[0].Tiers) != 2 {
		t.Fatalf("list: %v %+v", err, list)
	}
}

func TestCombo_Tiers_Validation(t *testing.T) {
	s, err := Open(t.Context(), "file:tiervalid?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.SaveCombo(t.Context(), RouteCombo{Name: "bad", Tiers: nil}); err == nil {
		t.Fatal("expected error for empty tiers")
	}
	if _, err := s.SaveCombo(t.Context(), RouteCombo{Name: "bad2", Tiers: []ComboTier{{Provider: "nope"}}}); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// TestStore_ProviderProxyPin verifies per-provider proxy selection:
// pinning stores mode+pool, pool mode without an id fails, a pool id on
// other modes is cleared, and UnpinPool resets pins to global rotation.
