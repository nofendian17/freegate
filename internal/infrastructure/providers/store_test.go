package providers

import "testing"

func TestStore_CRUD_AndMask(t *testing.T) {
	s, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(Provider{Name: "acme", BaseURL: "https://api.acme.test/v1", APIKeys: []string{"sk-live-123456"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected nonzero ID")
	}
	list, err := s.ListProviders()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if MaskKeys(list[0].APIKeys)[0] != "****3456" {
		t.Fatalf("keys not masked: %q", list[0].APIKeys[0])
	}
}

func TestProvider_Validate_RejectsBadName(t *testing.T) {
	s, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CreateProvider(Provider{Name: "Bad Name!", BaseURL: "https://x.test/v1", APIKeys: []string{"k"}}); err == nil {
		t.Fatal("expected validation error for bad name")
	}
}

func TestStore_Create_DefaultsRefreshSec(t *testing.T) {
	s, err := Open("file:defrefresh?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(Provider{Name: "defref", BaseURL: "https://r.test/v1", APIKeys: []string{"k"}, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.RefreshSec != 60 {
		t.Fatalf("expected RefreshSec 60, got %d", p.RefreshSec)
	}
	raw, err := s.GetProviderRaw(p.ID)
	if err != nil {
		t.Fatalf("get raw: %v", err)
	}
	if raw.RefreshSec != 60 {
		t.Fatalf("expected stored RefreshSec 60, got %d", raw.RefreshSec)
	}
}

func TestDeleteProvider_StripsComboMember(t *testing.T) {
	s, err := Open("file:delstrip?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(Provider{Name: "x", BaseURL: "https://x.test/v1", APIKeys: []string{"k"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := s.SaveCombo(RouteCombo{Name: "mix", Members: []string{"opencode", "custom:x"}})
	if err != nil {
		t.Fatalf("save combo: %v", err)
	}
	if err := s.DeleteProvider(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := s.ListCombos()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, x := range list {
		if x.ID == c.ID {
			if len(x.Members) != 1 || x.Members[0] != "opencode" {
				t.Fatalf("expected members [opencode], got %q", x.Members)
			}
			return
		}
	}
	t.Fatal("combo missing after provider delete")
}

func TestCombo_Update_PreservesActive(t *testing.T) {
	s, err := Open("file:updatecombo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(RouteCombo{Name: "upd", Members: []string{"opencode"}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.ActivateCombo(c.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	u, err := s.UpdateCombo(c.ID, RouteCombo{Name: "upd", Members: []string{"opencode", "kilo"}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if u.ID != c.ID {
		t.Fatalf("expected same ID %d, got %d", c.ID, u.ID)
	}
	if !u.IsActive {
		t.Fatal("expected IsActive preserved")
	}
	list, err := s.ListCombos()
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

func TestGetProvider_Masked_AndRaw(t *testing.T) {
	s, err := Open("file:getmask?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(Provider{Name: "maskme", BaseURL: "https://m.test/v1", APIKeys: []string{"sk-live-123456"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetProvider(p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.APIKeys) != 1 || got.APIKeys[0] != "****3456" {
		t.Fatalf("expected masked key, got %q", got.APIKeys)
	}
	raw, err := s.GetProviderRaw(p.ID)
	if err != nil {
		t.Fatalf("get raw: %v", err)
	}
	if len(raw.APIKeys) != 1 || raw.APIKeys[0] != "sk-live-123456" {
		t.Fatalf("expected raw key, got %q", raw.APIKeys)
	}
}

func TestCombo_SingleActive(t *testing.T) {
	s, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	a, _ := s.SaveCombo(RouteCombo{Name: "a", Members: []string{"opencode"}})
	b, _ := s.SaveCombo(RouteCombo{Name: "b", Members: []string{"kilo"}})
	if err := s.ActivateCombo(a.ID); err != nil {
		t.Fatalf("activate a: %v", err)
	}
	if err := s.ActivateCombo(b.ID); err != nil {
		t.Fatalf("activate b: %v", err)
	}
	active, err := s.ActiveCombo()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if active.Name != "b" {
		t.Fatalf("expected b active, got %q", active.Name)
	}
}

func TestActivateCombo_BadID_KeepsActive(t *testing.T) {
	s, err := Open("file:badactivate?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(RouteCombo{Name: "keep", Members: []string{"opencode"}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.ActivateCombo(c.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := s.ActivateCombo(c.ID + 9999); err == nil {
		t.Fatal("expected error for bad combo id")
	}
	active, err := s.ActiveCombo()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if active.Name != "keep" {
		t.Fatalf("expected keep still active, got %q", active.Name)
	}
}

func TestCombo_Tiers_CRUD(t *testing.T) {
	s, err := Open("file:tiercrud?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(RouteCombo{Name: "hemat", Tiers: []ComboTier{{Provider: "opencode"}, {Provider: "kilo", Model: "kilo-auto"}}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(c.Tiers) != 2 || c.Tiers[0].Provider != "opencode" {
		t.Fatalf("bad tiers: %+v", c.Tiers)
	}
	list, err := s.ListCombos()
	if err != nil || len(list) != 1 || len(list[0].Tiers) != 2 {
		t.Fatalf("list: %v %+v", err, list)
	}
}

func TestCombo_Tiers_Validation(t *testing.T) {
	s, err := Open("file:tiervalid?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.SaveCombo(RouteCombo{Name: "bad", Tiers: nil}); err == nil {
		t.Fatal("expected error for empty tiers")
	}
	if _, err := s.SaveCombo(RouteCombo{Name: "bad2", Tiers: []ComboTier{{Provider: "nope"}}}); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestCombo_Members_Migrated_To_Tiers(t *testing.T) {
	s, err := Open("file:tiermig?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.db.Exec("INSERT INTO route_combos (name, members, is_active) VALUES (?,?,?)", "legacy", `["opencode","custom:acme"]`, false).Error; err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := s.migrateMembersToTiers(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	list, err := s.ListCombos()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if len(list[0].Tiers) != 2 || list[0].Tiers[1].Provider != "custom:acme" {
		t.Fatalf("not migrated: %+v", list[0].Tiers)
	}
}
