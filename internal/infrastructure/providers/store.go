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
	ID         uint              `gorm:"primaryKey" json:"id"`
	Name       string            `gorm:"uniqueIndex;not null" json:"name"`
	BaseURL    string            `gorm:"not null" json:"base_url"`
	APIKeys    []string          `gorm:"serializer:json;not null" json:"-"`
	Headers    map[string]string `gorm:"serializer:json" json:"headers,omitempty"`
	ModelAllow []string          `gorm:"serializer:json" json:"model_allow,omitempty"`
	ModelBlock []string          `gorm:"serializer:json" json:"model_block,omitempty"`
	RefreshSec int               `gorm:"default:60" json:"refresh_sec"`
	// Priority controls list ordering only; runtime order comes
	// solely from combo tiers.
	Priority int  `json:"priority"`
	Enabled  bool `gorm:"default:true" json:"enabled"`
}

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
	return nil
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
	if err := db.AutoMigrate(&Provider{}, &RouteCombo{}); err != nil {
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
	if err := p.Validate(); err != nil {
		return Provider{}, err
	}
	p.ID = 0
	if err := s.db.Create(&p).Error; err != nil {
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
		if err != nil || !row.Enabled {
			return fmt.Errorf("tier %d: unknown or disabled provider %q", i+1, tr.Provider)
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
	cur, err := s.GetProviderRaw(id)
	if err != nil {
		return Provider{}, err
	}
	p.ID = cur.ID
	if p.RefreshSec == 0 {
		p.RefreshSec = 60
	}
	if err := p.Validate(); err != nil {
		return Provider{}, err
	}
	if err := s.db.Save(&p).Error; err != nil {
		return Provider{}, err
	}
	p.APIKeys = MaskKeys(p.APIKeys)
	return p, nil
}

func (s *Store) DeleteProvider(id uint) error {
	var cur Provider
	if err := s.db.First(&cur, id).Error; err != nil {
		return err
	}
	if err := s.db.Delete(&Provider{}, id).Error; err != nil {
		return err
	}
	member := "custom:" + cur.Name
	var combos []RouteCombo
	if err := s.db.Find(&combos).Error; err != nil {
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
			if err := s.db.Delete(&RouteCombo{}, c.ID).Error; err != nil {
				return err
			}
			continue
		}
		c.Tiers = keptTiers
		if err := s.db.Save(&c).Error; err != nil {
			return err
		}
	}
	return nil
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
