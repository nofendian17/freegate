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

func TestCombo_Update_PreservesActive(t *testing.T) {
	s, err := Open("file:updatecombo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(RouteCombo{Name: "upd", Members: []string{"a"}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.ActivateCombo(c.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	u, err := s.UpdateCombo(c.ID, RouteCombo{Name: "upd", Members: []string{"a", "b"}})
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
