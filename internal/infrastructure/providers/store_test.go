package providers

import (
	"errors"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestStore_CreateDisabled(t *testing.T) {
	s, err := Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.CreateProvider(Provider{Name: "disabled", BaseURL: "https://example.test/v1", APIKeys: []string{}, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled {
		t.Error("created provider must remain disabled")
	}
	raw, err := s.GetProviderRaw(p.ID)
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
				s, err := Open(t.TempDir() + "/providers.db")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				p, err := s.CreateProvider(Provider{Name: "before", BaseURL: "https://example.test/v1", APIKeys: []string{"test-key"}, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				original := []ComboTier{{Provider: "custom:before", Model: "first"}, {Provider: "opencode"}, {Provider: "custom:before", Model: "second"}}
				if _, err := s.SaveCombo(RouteCombo{Name: "mixed", Tiers: original}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SaveCombo(RouteCombo{Name: "solo", Tiers: []ComboTier{{Provider: "custom:before"}}}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SaveCombo(RouteCombo{Name: "untouched", Tiers: []ComboTier{{Provider: "kilo"}}}); err != nil {
					t.Fatal(err)
				}
				before, err := s.ListCombos()
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
					_, err = s.UpdateProvider(p.ID, p)
				} else {
					err = s.DeleteProvider(p.ID)
				}
				if fail {
					if !errors.Is(err, injected) {
						t.Fatalf("expected injected failure, got %v", err)
					}
					raw, err := s.GetProviderRaw(p.ID)
					if err != nil || raw.Name != "before" {
						t.Fatalf("provider not rolled back: %+v, %v", raw, err)
					}
					got, err := s.ListCombos()
					if err != nil || !reflect.DeepEqual(got, before) {
						t.Fatalf("combos not rolled back: %+v, %v", got, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := s.ListCombos()
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
					if _, err := s.GetProviderRaw(p.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
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

func TestStore_PoolCRUD(t *testing.T) {
	s, err := Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.CreatePool(ProxyPool{Name: "relay-1", ProxyURL: "https://relay-1.example.com", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected nonzero ID")
	}
	if _, err := s.CreatePool(ProxyPool{Name: "bad", ProxyURL: "not-a-url"}); err == nil {
		t.Fatal("expected validation error for bad proxy_url")
	}
	list, err := s.ListPools()
	if err != nil || len(list) != 1 || list[0].ProxyURL != "https://relay-1.example.com" {
		t.Fatalf("list: %v %+v", err, list)
	}
	got, err := s.GetPool(p.ID)
	if err != nil || got.Name != "relay-1" {
		t.Fatalf("get: %v %+v", err, got)
	}
	upd, err := s.UpdatePool(p.ID, ProxyPool{Name: "relay-1", ProxyURL: "https://relay-2.example.com", NoProxy: "example.com", Enabled: false})
	if err != nil || upd.ProxyURL != "https://relay-2.example.com" || upd.NoProxy != "example.com" || upd.Enabled {
		t.Fatalf("update: %v %+v", err, upd)
	}
	if err := s.DeletePool(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := s.ListPools(); len(list) != 0 {
		t.Fatalf("expected empty after delete, got %+v", list)
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
	c, err := s.SaveCombo(RouteCombo{Name: "mix", Tiers: []ComboTier{{Provider: "opencode"}, {Provider: "custom:x"}}})
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
			if len(x.Tiers) != 1 || x.Tiers[0].Provider != "opencode" {
				t.Fatalf("expected tiers [opencode], got %+v", x.Tiers)
			}
			return
		}
	}
	t.Fatal("combo missing after provider delete")
}

func TestDeleteProvider_EmptiedCombo_Deleted(t *testing.T) {
	s, err := Open("file:delempty?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := s.CreateProvider(Provider{Name: "solo", BaseURL: "https://solo.test/v1", APIKeys: []string{"k"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := s.SaveCombo(RouteCombo{Name: "only", Tiers: []ComboTier{{Provider: "custom:solo"}}})
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
			t.Fatalf("emptied combo row not deleted: %+v", x)
		}
	}
}

func TestSaveCombo_UnknownCustom_Rejected(t *testing.T) {
	s, err := Open("file:ghostcombo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.SaveCombo(RouteCombo{Name: "ghost", Tiers: []ComboTier{{Provider: "custom:ghost"}}}); err == nil {
		t.Fatal("expected error for unknown custom provider")
	}
	p, err := s.CreateProvider(Provider{Name: "off", BaseURL: "https://off.test/v1", APIKeys: []string{"k"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p.Enabled = false
	if _, err := s.UpdateProvider(p.ID, p); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := s.SaveCombo(RouteCombo{Name: "ghost2", Tiers: []ComboTier{{Provider: "custom:off"}}}); err == nil {
		t.Fatal("expected error for disabled custom provider")
	}
	if _, err := s.UpdateCombo(1, RouteCombo{Name: "ghost3", Tiers: []ComboTier{{Provider: "custom:ghost"}}}); err == nil {
		t.Fatal("expected error for unknown custom provider on update")
	}
}

func TestSaveCombo_TrimsTierProviders(t *testing.T) {
	s, err := Open("file:trimcombo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(RouteCombo{Name: "trim", Tiers: []ComboTier{{Provider: " opencode "}}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if c.Tiers[0].Provider != "opencode" {
		t.Fatalf("provider not trimmed: %q", c.Tiers[0].Provider)
	}
}

func TestOpen_NullTiers_Backfilled(t *testing.T) {
	path := t.TempDir() + "/providers.db"
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if err := raw.Exec(`INSERT INTO route_combos (name, tiers) VALUES (?, NULL)`, "legacy-null").Error; err != nil {
		t.Fatalf("seed null tiers: %v", err)
	}
	sqlDB, _ := raw.DB()
	_ = sqlDB.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list, err := s2.ListCombos()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || len(list[0].Tiers) != 0 {
		t.Fatalf("expected one combo with empty tiers, got %+v", list)
	}
}

func TestCombo_Update_PreservesID(t *testing.T) {
	s, err := Open("file:updatecombo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := s.SaveCombo(RouteCombo{Name: "upd", Tiers: []ComboTier{{Provider: "opencode"}}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	u, err := s.UpdateCombo(c.ID, RouteCombo{Name: "upd", Tiers: []ComboTier{{Provider: "opencode"}, {Provider: "kilo"}}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if u.ID != c.ID {
		t.Fatalf("expected same ID %d, got %d", c.ID, u.ID)
	}
	if len(u.Tiers) != 2 {
		t.Fatalf("expected 2 tiers, got %+v", u.Tiers)
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
	dsn := "file:tiermig?mode=memory&cache=shared"
	raw, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if err := raw.Exec(`CREATE TABLE route_combos (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, tiers TEXT, members TEXT, is_active NUMERIC)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := raw.Exec(`INSERT INTO route_combos (name, tiers, members, is_active) VALUES (?,?,?,?)`, "legacy", `[]`, `["opencode","custom:acme"]`, false).Error; err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	sqlDB, _ := raw.DB()
	defer sqlDB.Close()
	s := &Store{db: raw}
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

// TestStore_ProviderProxyPin verifies per-provider proxy selection:
// pinning stores mode+pool, pool mode without an id fails, a pool id on
// other modes is cleared, and UnpinPool resets pins to global rotation.
func TestStore_ProviderProxyPin(t *testing.T) {
	s, err := Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pool, err := s.CreatePool(ProxyPool{Name: "edge-1", ProxyURL: "https://relay.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CreateProvider(Provider{Name: "pinned", BaseURL: "https://example.test/v1", APIKeys: []string{"k"}, Models: []string{"m"}, Enabled: true, ProxyMode: "pool", ProxyPoolID: &pool.ID})
	if err != nil {
		t.Fatalf("create pinned: %v", err)
	}
	if pinned.EffectiveProxyMode() != ProxyModePool || pinned.ProxyPoolID == nil || *pinned.ProxyPoolID != pool.ID {
		t.Fatalf("pin not stored: %+v", pinned)
	}
	if _, err := s.CreateProvider(Provider{Name: "noid", BaseURL: "https://example.test/v1", APIKeys: []string{"k"}, Enabled: true, ProxyMode: "pool"}); err == nil {
		t.Fatal("expected pool mode without id to fail")
	}
	direct, err := s.CreateProvider(Provider{Name: "direct", BaseURL: "https://example.test/v1", APIKeys: []string{"k"}, Enabled: true, ProxyMode: "direct", ProxyPoolID: &pool.ID})
	if err != nil {
		t.Fatalf("create direct: %v", err)
	}
	if direct.ProxyPoolID != nil {
		t.Fatalf("pool id must be cleared outside pool mode: %+v", direct)
	}
	if err := s.UnpinPool(pool.ID); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	raw, err := s.GetProviderRaw(pinned.ID)
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
func TestStore_BuiltinProxy(t *testing.T) {
	s, err := Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	b, err := s.GetBuiltinProxy("opencode")
	if err != nil || b.Name != "opencode" || NormalizeProxyMode(b.ProxyMode) != ProxyModeGlobal || b.ProxyPoolID != nil {
		t.Fatalf("default must be global: %+v %v", b, err)
	}
	if _, err := s.SetBuiltinProxy("nope", "direct", nil); err == nil {
		t.Fatal("expected unknown builtin to fail")
	}
	if _, err := s.SetBuiltinProxy("kilo", "pool", nil); err == nil {
		t.Fatal("expected pool mode without id to fail")
	}
	bad := uint(999999)
	if _, err := s.SetBuiltinProxy("kilo", "pool", &bad); err == nil {
		t.Fatal("expected missing pool to fail")
	}
	pool, err := s.CreatePool(ProxyPool{Name: "edge-1", ProxyURL: "https://relay.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBuiltinProxy("kilo", "pool", &pool.ID); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	b, err = s.GetBuiltinProxy("kilo")
	if err != nil || b.ProxyMode != ProxyModePool || b.ProxyPoolID == nil || *b.ProxyPoolID != pool.ID {
		t.Fatalf("pin not stored: %+v %v", b, err)
	}
	if _, err := s.SetBuiltinProxy("kilo", "direct", &pool.ID); err != nil {
		t.Fatalf("set direct: %v", err)
	}
	b, err = s.GetBuiltinProxy("kilo")
	if err != nil || b.ProxyMode != ProxyModeDirect || b.ProxyPoolID != nil {
		t.Fatalf("direct must clear pin: %+v %v", b, err)
	}
	if _, err := s.SetBuiltinProxy("llm7", "pool", &pool.ID); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	if err := s.UnpinPool(pool.ID); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	b, err = s.GetBuiltinProxy("llm7")
	if err != nil || NormalizeProxyMode(b.ProxyMode) != ProxyModeGlobal || b.ProxyPoolID != nil {
		t.Fatalf("unpin must reset builtin to global: %+v %v", b, err)
	}
	list, err := s.ListBuiltinProxies()
	if err != nil || len(list) != len(KnownBuiltins) {
		t.Fatalf("list builtin: %v %+v", err, list)
	}
}

