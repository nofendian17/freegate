package registry

import (
	"errors"
	"gorm.io/gorm"
	"reflect"
	"testing"
)

func TestStore_CreateDisabled(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.CreateProvider(t.Context(), Provider{Name: "disabled", BaseURL: "https://example.test/v1", APIKeys: []string{}, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled {
		t.Error("created provider must remain disabled")
	}
	raw, err := s.GetProviderRaw(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Enabled {
		t.Error("stored provider must remain disabled")
	}
}

func TestStore_ProviderComboMutation(t *testing.T) {
	for _, operation := range []string{"rename", "delete"} {
		for _, fail := range []bool{false, true} {
			name := operation
			if fail {
				name += "-rollback"
			}
			t.Run(name, func(t *testing.T) {
				s, err := Open(t.Context(), t.TempDir()+"/providers.db")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				p, err := s.CreateProvider(t.Context(), Provider{Name: "before", BaseURL: "https://example.test/v1", APIKeys: []string{"test-key"}, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				original := []ComboTier{{Provider: "custom:before", Model: "first"}, {Provider: "opencode"}, {Provider: "custom:before", Model: "second"}}
				if _, err := s.SaveCombo(t.Context(), RouteCombo{Name: "mixed", Tiers: original}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SaveCombo(t.Context(), RouteCombo{Name: "solo", Tiers: []ComboTier{{Provider: "custom:before"}}}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SaveCombo(t.Context(), RouteCombo{Name: "untouched", Tiers: []ComboTier{{Provider: "kilo"}}}); err != nil {
					t.Fatal(err)
				}
				before, err := s.ListCombos(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				injected := errors.New("combo write failed")
				if fail {
					if err := s.db.Callback().Update().Before("gorm:update").Register("test:combo-failure", func(tx *gorm.DB) {
						if tx.Statement.Table == "route_combos" {
							tx.AddError(injected)
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				if operation == "rename" {
					p.Name = "after"
					p.APIKeys = []string{"test-key"}
					_, err = s.UpdateProvider(t.Context(), p.ID, p)
				} else {
					err = s.DeleteProvider(t.Context(), p.ID)
				}
				if fail {
					if !errors.Is(err, injected) {
						t.Fatalf("expected injected failure, got %v", err)
					}
					raw, err := s.GetProviderRaw(t.Context(), p.ID)
					if err != nil || raw.Name != "before" {
						t.Fatalf("provider not rolled back: %+v, %v", raw, err)
					}
					got, err := s.ListCombos(t.Context())
					if err != nil || !reflect.DeepEqual(got, before) {
						t.Fatalf("combos not rolled back: %+v, %v", got, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := s.ListCombos(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				want := before
				if operation == "rename" {
					for i := range want {
						for j := range want[i].Tiers {
							if want[i].Tiers[j].Provider == "custom:before" {
								want[i].Tiers[j].Provider = "custom:after"
							}
						}
					}
				} else {
					if _, err := s.GetProviderRaw(t.Context(), p.ID); !errors.Is(err, ErrNotFound) {
						t.Fatalf("provider still exists: %v", err)
					}
					want = []RouteCombo{before[0], before[2]}
					want[0].Tiers = []ComboTier{{Provider: "opencode"}}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("combos = %+v, want %+v", got, want)
				}
			})
		}
	}
}

func TestStore_CRUD_AndMask(t *testing.T) {
	s, err := Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(t.Context(), Provider{Name: "acme", BaseURL: "https://api.acme.test/v1", APIKeys: []string{"sk-live-123456"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected nonzero ID")
	}
	list, err := s.ListProviders(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if MaskKeys(list[0].APIKeys)[0] != "****3456" {
		t.Fatalf("keys not masked: %q", list[0].APIKeys[0])
	}
}

func TestStore_Create_DefaultsRefreshSec(t *testing.T) {
	s, err := Open(t.Context(), "file:defrefresh?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(t.Context(), Provider{Name: "defref", BaseURL: "https://r.test/v1", APIKeys: []string{"k"}, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.RefreshSec != 60 {
		t.Fatalf("expected RefreshSec 60, got %d", p.RefreshSec)
	}
	raw, err := s.GetProviderRaw(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("get raw: %v", err)
	}
	if raw.RefreshSec != 60 {
		t.Fatalf("expected stored RefreshSec 60, got %d", raw.RefreshSec)
	}
}

func TestDeleteProvider_StripsComboMember(t *testing.T) {
	s, err := Open(t.Context(), "file:delstrip?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(t.Context(), Provider{Name: "x", BaseURL: "https://x.test/v1", APIKeys: []string{"k"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := s.SaveCombo(t.Context(), RouteCombo{Name: "mix", Tiers: []ComboTier{{Provider: "opencode"}, {Provider: "custom:x"}}})
	if err != nil {
		t.Fatalf("save combo: %v", err)
	}
	if err := s.DeleteProvider(t.Context(), p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := s.ListCombos(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, x := range list {
		if x.ID == c.ID {
			if len(x.Tiers) != 1 || x.Tiers[0].Provider != "opencode" {
				t.Fatalf("expected tiers [opencode], got %+v", x.Tiers)
			}
			return
		}
	}
	t.Fatal("combo missing after provider delete")
}

func TestDeleteProvider_EmptiedCombo_Deleted(t *testing.T) {
	s, err := Open(t.Context(), "file:delempty?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(t.Context(), Provider{Name: "solo", BaseURL: "https://solo.test/v1", APIKeys: []string{"k"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := s.SaveCombo(t.Context(), RouteCombo{Name: "only", Tiers: []ComboTier{{Provider: "custom:solo"}}})
	if err != nil {
		t.Fatalf("save combo: %v", err)
	}
	if err := s.DeleteProvider(t.Context(), p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := s.ListCombos(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, x := range list {
		if x.ID == c.ID {
			t.Fatalf("emptied combo row not deleted: %+v", x)
		}
	}
}

func TestGetProvider_Masked_AndRaw(t *testing.T) {
	s, err := Open(t.Context(), "file:getmask?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(t.Context(), Provider{Name: "maskme", BaseURL: "https://m.test/v1", APIKeys: []string{"sk-live-123456"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetProvider(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.APIKeys) != 1 || got.APIKeys[0] != "****3456" {
		t.Fatalf("expected masked key, got %q", got.APIKeys)
	}
	raw, err := s.GetProviderRaw(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("get raw: %v", err)
	}
	if len(raw.APIKeys) != 1 || raw.APIKeys[0] != "sk-live-123456" {
		t.Fatalf("expected raw key, got %q", raw.APIKeys)
	}
}

func TestStore_ProviderProxyPin(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pool, err := s.CreatePool(t.Context(), ProxyPool{Name: "edge-1", ProxyURL: "https://relay.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CreateProvider(t.Context(), Provider{Name: "pinned", BaseURL: "https://example.test/v1", APIKeys: []string{"k"}, Models: []string{"m"}, Enabled: true, ProxyMode: "pool", ProxyPoolID: &pool.ID})
	if err != nil {
		t.Fatalf("create pinned: %v", err)
	}
	if pinned.EffectiveProxyMode() != ProxyModePool || pinned.ProxyPoolID == nil || *pinned.ProxyPoolID != pool.ID {
		t.Fatalf("pin not stored: %+v", pinned)
	}
	if _, err := s.CreateProvider(t.Context(), Provider{Name: "noid", BaseURL: "https://example.test/v1", APIKeys: []string{"k"}, Enabled: true, ProxyMode: "pool"}); err == nil {
		t.Fatal("expected pool mode without id to fail")
	}
	if _, err := s.CreateProvider(t.Context(), Provider{Name: "badmode", BaseURL: "https://example.test/v1", APIKeys: []string{"k"}, Enabled: true, ProxyMode: "sideways"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown proxy mode error = %v, want ErrInvalidArgument", err)
	}
	direct, err := s.CreateProvider(t.Context(), Provider{Name: "direct", BaseURL: "https://example.test/v1", APIKeys: []string{"k"}, Enabled: true, ProxyMode: "direct", ProxyPoolID: &pool.ID})
	if err != nil {
		t.Fatalf("create direct: %v", err)
	}
	if direct.ProxyPoolID != nil {
		t.Fatalf("pool id must be cleared outside pool mode: %+v", direct)
	}
	if err := s.UnpinPool(t.Context(), pool.ID); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	raw, err := s.GetProviderRaw(t.Context(), pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.EffectiveProxyMode() != ProxyModeGlobal || raw.ProxyPoolID != nil {
		t.Fatalf("unpin must reset to global: %+v", raw)
	}
}

// TestStore_BuiltinProxy verifies builtin relay selection storage:
// missing rows default to global, unknown names fail, pool pins require
// an existing pool, and UnpinPool resets builtin pins too.
