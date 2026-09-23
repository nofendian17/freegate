package providers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var nameRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

type Provider struct {
	ID      uint              `gorm:"primaryKey" json:"id"`
	Name    string            `gorm:"uniqueIndex;not null" json:"name"`
	BaseURL string            `gorm:"not null" json:"base_url"`
	APIKeys []string          `gorm:"serializer:json;not null" json:"-"`
	Headers map[string]string `gorm:"serializer:json" json:"headers,omitempty"`
	// Models is the explicit user-curated selection: only these model IDs
	// are stored and routed. The background refresh never adds models on
	// its own; it only refreshes metadata for the selected ones.
	// A nil slice marks a legacy row (saved before the selection
	// feature): the whole catalog routes, preserving the pre-upgrade
	// semantic of empty allow+block. An explicit empty slice routes
	// nothing. No omitempty so nil (null) stays distinguishable from
	// empty ([]) on the wire.
	// (Legacy model_allow/model_block columns may still exist in old DB
	// files; they are no longer read. AutoMigrate never drops columns.)
	Models     []string `gorm:"serializer:json" json:"models"`
	RefreshSec int      `gorm:"default:60" json:"refresh_sec"`
	// Priority controls list ordering only; runtime order comes
	// solely from combo tiers.
	Priority int  `json:"priority"`
	Enabled  bool `gorm:"default:true" json:"enabled"`
	// ProxyMode selects edge-relay behavior for this provider:
	// "" (zero value) follows the global pool rotation, "direct"
	// skips all relays, "pool" pins to ProxyPoolID. Existing rows
	// default to global, preserving current behavior.
	ProxyMode string `json:"proxy_mode,omitempty"`
	// ProxyPoolID pins the provider to one pool when ProxyMode is
	// "pool". Nil otherwise (cleared on save for other modes).
	ProxyPoolID *uint `json:"proxy_pool_id,omitempty"`
}

// Proxy selection modes for Provider.ProxyMode.
const (
	ProxyModeGlobal = ""
	ProxyModeDirect = "direct"
	ProxyModePool   = "pool"
)

// NormalizeProxyMode trims and lowercases the mode, mapping the "global"
// alias to the zero value so stored rows stay canonical.
func NormalizeProxyMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ProxyModeDirect:
		return ProxyModeDirect
	case ProxyModePool:
		return ProxyModePool
	default:
		return ProxyModeGlobal
	}
}

// EffectiveProxyMode reports the canonical mode ("", "direct", "pool").
func (p *Provider) EffectiveProxyMode() string { return NormalizeProxyMode(p.ProxyMode) }

type ComboTier struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
}

var KnownBuiltins = []string{"opencode", "kilo", "llm7"}

var knownBuiltin = map[string]bool{"opencode": true, "kilo": true, "llm7": true}

func IsBuiltin(name string) bool { return knownBuiltin[name] }

func validTierProvider(p string) bool {
	if knownBuiltin[p] {
		return true
	}
	if strings.HasPrefix(p, "custom:") {
		return nameRe.MatchString(strings.TrimPrefix(p, "custom:"))
	}
	return false
}

func validateTiers(tiers []ComboTier) error {
	if len(tiers) == 0 {
		return fmt.Errorf("combo needs at least one tier")
	}
	for i, tr := range tiers {
		if !validTierProvider(strings.TrimSpace(tr.Provider)) {
			return fmt.Errorf("tier %d: unknown provider %q", i+1, tr.Provider)
		}
	}
	return nil
}

type RouteCombo struct {
	ID    uint        `gorm:"primaryKey" json:"id"`
	Name  string      `gorm:"uniqueIndex;not null" json:"name"`
	Tiers []ComboTier `gorm:"serializer:json" json:"tiers"`
}

type ProxyPool struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"uniqueIndex;not null" json:"name"`
	ProxyURL    string `gorm:"not null" json:"proxy_url"`
	NoProxy     string `json:"no_proxy,omitempty"`
	StrictProxy bool   `json:"strict_proxy"`
	Enabled     bool   `gorm:"default:true" json:"enabled"`
	TestStatus  string `json:"test_status,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

// BuiltinProxy stores the edge-relay selection of a builtin upstream
// (opencode, kilo, llm7). Absent row means global rotation. Same mode
// semantics as Provider.ProxyMode/ProxyPoolID.
type BuiltinProxy struct {
	Name        string `gorm:"primaryKey" json:"name"`
	ProxyMode   string `json:"proxy_mode,omitempty"`
	ProxyPoolID *uint  `json:"proxy_pool_id,omitempty"`
}

// MarkPoolTest records the outcome of a pool probe.
func (s *Store) MarkPoolTest(id uint, ok bool, lastErr string) error {
	status := "active"
	if !ok {
		status = "error"
	}
	return s.db.Model(&ProxyPool{}).Where("id = ?", id).Updates(map[string]any{
		"test_status": status, "last_error": lastErr,
	}).Error
}

func (p *ProxyPool) Validate() error {
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("name must match ^[a-z0-9-]{1,64}$")
	}
	u := strings.ToLower(strings.TrimSpace(p.ProxyURL))
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return fmt.Errorf("proxy_url must be http(s) URL")
	}
	return nil
}

type legacyComboRow struct {
	ID      uint
	Tiers   []ComboTier `gorm:"serializer:json"`
	Members []string    `gorm:"serializer:json"`
}

func (p *Provider) Validate() error {
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("name must match ^[a-z0-9-]{1,64}$")
	}
	u := strings.ToLower(strings.TrimSpace(p.BaseURL))
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return fmt.Errorf("base_url must be http(s) URL")
	}
	if p.Enabled {
		n := 0
		for _, k := range p.APIKeys {
			if strings.TrimSpace(k) != "" {
				n++
			}
		}
		if n == 0 {
			return fmt.Errorf("enabled provider needs at least one api key")
		}
	}
	if p.RefreshSec < 10 || p.RefreshSec > 3600 {
		return fmt.Errorf("refresh_sec must be 10..3600")
	}
	if NormalizeProxyMode(p.ProxyMode) == ProxyModePool && p.ProxyPoolID == nil {
		return fmt.Errorf("proxy_pool_id is required when proxy_mode is pool")
	}
	return nil
}

// NormalizeModels trims, drops empties, and dedupes model IDs while
// preserving order. Used for the explicit per-provider selection.
// A nil input stays nil so legacy rows (whole catalog routes) survive
// a normalize round-trip; use an explicit empty slice to route nothing.
func NormalizeModels(in []string) []string {
	if in == nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, m := range in {
		if m = strings.TrimSpace(m); m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func MaskKeys(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		if len(k) <= 4 {
			out[i] = "****"
			continue
		}
		out[i] = "****" + k[len(k)-4:]
	}
	return out
}

type Store struct{ db *gorm.DB }

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = "./data/providers.db"
	}
	if !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir db dir: %w", err)
		}
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open providers db: %w", err)
	}
	if err := db.AutoMigrate(&Provider{}, &RouteCombo{}, &ProxyPool{}, &BuiltinProxy{}, &ClientKey{}); err != nil {
		return nil, fmt.Errorf("migrate providers db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrateMembersToTiers(); err != nil {
		return nil, fmt.Errorf("migrate members to tiers: %w", err)
	}
	return s, nil
}

func (s *Store) backfillNullTiers() error {
	return s.db.Table("route_combos").Where("tiers IS NULL").Update("tiers", "[]").Error
}

func (s *Store) migrateMembersToTiers() error {
	if !s.db.Migrator().HasColumn(&RouteCombo{}, "members") {
		return s.backfillNullTiers()
	}
	var rows []legacyComboRow
	if err := s.db.Table("route_combos").Find(&rows).Error; err != nil {
		return err
	}
	for _, c := range rows {
		if len(c.Tiers) > 0 {
			continue
		}
		tiers := make([]ComboTier, 0, len(c.Members))
		for _, m := range c.Members {
			tiers = append(tiers, ComboTier{Provider: m})
		}
		raw, err := json.Marshal(tiers)
		if err != nil {
			return err
		}
		if err := s.db.Table("route_combos").Where("id = ?", c.ID).Update("tiers", string(raw)).Error; err != nil {
			return err
		}
	}
	return s.backfillNullTiers()
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (s *Store) CreateProvider(p Provider) (Provider, error) {
	if p.RefreshSec == 0 {
		p.RefreshSec = 60
	}
	p.Models = NormalizeModels(p.Models)
	p.ProxyMode = NormalizeProxyMode(p.ProxyMode)
	if p.ProxyMode != ProxyModePool {
		p.ProxyPoolID = nil
	}
	if err := p.Validate(); err != nil {
		return Provider{}, err
	}
	p.ID = 0
	enabled := p.Enabled
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&p).Error; err != nil {
			return err
		}
		// GORM replaces false with the schema default on insert.
		if !enabled {
			p.Enabled = false
			return tx.Model(&p).Update("enabled", false).Error
		}
		return nil
	}); err != nil {
		return Provider{}, err
	}
	p.APIKeys = MaskKeys(p.APIKeys)
	return p, nil
}

func (s *Store) ListProviders() ([]Provider, error) {
	var out []Provider
	if err := s.db.Order("priority asc, name asc").Find(&out).Error; err != nil {
		return nil, err
	}
	for i := range out {
		out[i].APIKeys = MaskKeys(out[i].APIKeys)
	}
	return out, nil
}

func (s *Store) GetProvider(id uint) (Provider, error) {
	p, err := s.GetProviderRaw(id)
	if err != nil {
		return Provider{}, err
	}
	p.APIKeys = MaskKeys(p.APIKeys)
	return p, nil
}

// GetProviderByName returns the provider with raw (unmasked) API keys.
func (s *Store) GetProviderByName(name string) (Provider, error) {
	var p Provider
	if err := s.db.Where("name = ?", name).First(&p).Error; err != nil {
		return Provider{}, err
	}
	return p, nil
}

func (s *Store) checkTiersExist(tiers []ComboTier) error {
	for i, tr := range tiers {
		p := strings.TrimSpace(tr.Provider)
		if IsBuiltin(p) {
			continue
		}
		if !strings.HasPrefix(p, "custom:") {
			continue
		}
		row, err := s.GetProviderByName(strings.TrimPrefix(p, "custom:"))
		if err != nil {
			return fmt.Errorf("tier %d: failed to look up provider %q: %w", i+1, tr.Provider, err)
		}
		if !row.Enabled {
			return fmt.Errorf("tier %d: provider %q is disabled", i+1, tr.Provider)
		}
	}
	return nil
}

// GetProviderRaw returns the provider with raw (unmasked) API keys.
// Internal-only: for the manager/dialer. API responses must use GetProvider.
func (s *Store) GetProviderRaw(id uint) (Provider, error) {
	var p Provider
	if err := s.db.First(&p, id).Error; err != nil {
		return Provider{}, err
	}
	return p, nil
}

func (s *Store) UpdateProvider(id uint, p Provider) (Provider, error) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cur Provider
		if err := tx.First(&cur, id).Error; err != nil {
			return err
		}
		p.ID = cur.ID
		if p.RefreshSec == 0 {
			p.RefreshSec = 60
		}
		p.Models = NormalizeModels(p.Models)
		p.ProxyMode = NormalizeProxyMode(p.ProxyMode)
		if p.ProxyMode != ProxyModePool {
			p.ProxyPoolID = nil
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		if p.Name == cur.Name {
			return nil
		}
		var combos []RouteCombo
		if err := tx.Find(&combos).Error; err != nil {
			return err
		}
		for _, c := range combos {
			changed := false
			for i := range c.Tiers {
				if c.Tiers[i].Provider == "custom:"+cur.Name {
					c.Tiers[i].Provider = "custom:" + p.Name
					changed = true
				}
			}
			if changed {
				if err := tx.Save(&c).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Provider{}, err
	}
	p.APIKeys = MaskKeys(p.APIKeys)
	return p, nil
}

func (s *Store) DeleteProvider(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cur Provider
		if err := tx.First(&cur, id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&Provider{}, id).Error; err != nil {
			return err
		}
		member := "custom:" + cur.Name
		var combos []RouteCombo
		if err := tx.Find(&combos).Error; err != nil {
			return err
		}
		for _, c := range combos {
			var keptTiers []ComboTier
			changed := false
			for _, tr := range c.Tiers {
				if tr.Provider == member {
					changed = true
					continue
				}
				keptTiers = append(keptTiers, tr)
			}
			if !changed {
				continue
			}
			if len(keptTiers) == 0 {
				if err := tx.Delete(&RouteCombo{}, c.ID).Error; err != nil {
					return err
				}
				continue
			}
			c.Tiers = keptTiers
			if err := tx.Save(&c).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListCombos() ([]RouteCombo, error) {
	var out []RouteCombo
	if err := s.db.Order("name asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) SaveCombo(c RouteCombo) (RouteCombo, error) {
	if !nameRe.MatchString(c.Name) {
		return RouteCombo{}, fmt.Errorf("combo needs valid name")
	}
	for i := range c.Tiers {
		c.Tiers[i].Provider = strings.TrimSpace(c.Tiers[i].Provider)
	}
	if err := validateTiers(c.Tiers); err != nil {
		return RouteCombo{}, err
	}
	if err := s.checkTiersExist(c.Tiers); err != nil {
		return RouteCombo{}, err
	}
	c.ID = 0
	if err := s.db.Create(&c).Error; err != nil {
		return RouteCombo{}, err
	}
	return c, nil
}

func (s *Store) UpdateCombo(id uint, c RouteCombo) (RouteCombo, error) {
	var cur RouteCombo
	if err := s.db.First(&cur, id).Error; err != nil {
		return RouteCombo{}, err
	}
	if !nameRe.MatchString(c.Name) {
		return RouteCombo{}, fmt.Errorf("combo needs valid name")
	}
	for i := range c.Tiers {
		c.Tiers[i].Provider = strings.TrimSpace(c.Tiers[i].Provider)
	}
	if err := validateTiers(c.Tiers); err != nil {
		return RouteCombo{}, err
	}
	if err := s.checkTiersExist(c.Tiers); err != nil {
		return RouteCombo{}, err
	}
	c.ID = cur.ID
	if err := s.db.Save(&c).Error; err != nil {
		return RouteCombo{}, err
	}
	return c, nil
}

func (s *Store) DeleteCombo(id uint) error { return s.db.Delete(&RouteCombo{}, id).Error }

func (s *Store) CreatePool(p ProxyPool) (ProxyPool, error) {
	if err := p.Validate(); err != nil {
		return ProxyPool{}, err
	}
	p.ID = 0
	enabled := p.Enabled
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&p).Error; err != nil {
			return err
		}
		if !enabled {
			p.Enabled = false
			return tx.Model(&p).Update("enabled", false).Error
		}
		return nil
	}); err != nil {
		return ProxyPool{}, err
	}
	return p, nil
}

func (s *Store) ListPools() ([]ProxyPool, error) {
	var out []ProxyPool
	if err := s.db.Order("name asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetPool(id uint) (ProxyPool, error) {
	var p ProxyPool
	if err := s.db.First(&p, id).Error; err != nil {
		return ProxyPool{}, err
	}
	return p, nil
}

func (s *Store) UpdatePool(id uint, p ProxyPool) (ProxyPool, error) {
	var cur ProxyPool
	if err := s.db.First(&cur, id).Error; err != nil {
		return ProxyPool{}, err
	}
	p.ID = cur.ID
	if err := p.Validate(); err != nil {
		return ProxyPool{}, err
	}
	if err := s.db.Save(&p).Error; err != nil {
		return ProxyPool{}, err
	}
	return p, nil
}

// DeletePool removes a pool and resets every pin on it (custom providers
// and builtins) back to the global rotation in one transaction, so a
// failure can never leave a half-deleted pool with dangling pins.
func (s *Store) DeletePool(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&ProxyPool{}, id).Error; err != nil {
			return err
		}
		reset := map[string]any{"proxy_mode": ProxyModeGlobal, "proxy_pool_id": nil}
		if err := tx.Model(&Provider{}).Where("proxy_pool_id = ?", id).Updates(reset).Error; err != nil {
			return err
		}
		return tx.Model(&BuiltinProxy{}).Where("proxy_pool_id = ?", id).Updates(reset).Error
	})
}

// UnpinPool resets providers pinned to the given pool back to the global
// rotation. Kept for callers that reset pins without deleting the pool;
// DeletePool covers the delete path transactionally above.
func (s *Store) UnpinPool(poolID uint) error {
	if err := s.db.Model(&Provider{}).Where("proxy_pool_id = ?", poolID).Updates(map[string]any{
		"proxy_mode": ProxyModeGlobal, "proxy_pool_id": nil,
	}).Error; err != nil {
		return err
	}
	return s.db.Model(&BuiltinProxy{}).Where("proxy_pool_id = ?", poolID).Updates(map[string]any{
		"proxy_mode": ProxyModeGlobal, "proxy_pool_id": nil,
	}).Error
}

// GetBuiltinProxy returns the stored relay selection for a builtin
// upstream. A missing row means global rotation (zero value, nil error).
func (s *Store) GetBuiltinProxy(name string) (BuiltinProxy, error) {
	var b BuiltinProxy
	if err := s.db.First(&b, "name = ?", name).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return BuiltinProxy{Name: name}, nil
		}
		return BuiltinProxy{}, err
	}
	b.ProxyMode = NormalizeProxyMode(b.ProxyMode)
	if b.ProxyMode != ProxyModePool {
		b.ProxyPoolID = nil
	}
	return b, nil
}

// ListBuiltinProxies returns the relay selection for every known builtin,
// filling global defaults for unconfigured ones.
func (s *Store) ListBuiltinProxies() ([]BuiltinProxy, error) {
	out := make([]BuiltinProxy, 0, len(KnownBuiltins))
	for _, name := range KnownBuiltins {
		b, err := s.GetBuiltinProxy(name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// SetBuiltinProxy stores the relay selection for a builtin upstream.
// Pool mode requires an existing pool; other modes clear the pin.
func (s *Store) SetBuiltinProxy(name, mode string, poolID *uint) (BuiltinProxy, error) {
	if !IsBuiltin(name) {
		return BuiltinProxy{}, fmt.Errorf("unknown builtin provider %q", name)
	}
	mode = NormalizeProxyMode(mode)
	if mode != ProxyModePool {
		poolID = nil
	} else {
		if poolID == nil {
			return BuiltinProxy{}, fmt.Errorf("proxy_pool_id is required when proxy_mode is pool")
		}
		if _, err := s.GetPool(*poolID); err != nil {
			return BuiltinProxy{}, fmt.Errorf("proxy pool %d not found", *poolID)
		}
	}
	b := BuiltinProxy{Name: name, ProxyMode: mode, ProxyPoolID: poolID}
	if err := s.db.Save(&b).Error; err != nil {
		return BuiltinProxy{}, err
	}
	return b, nil
}
