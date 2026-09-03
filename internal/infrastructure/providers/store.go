package providers

import (
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
	Priority   int               `json:"priority"`
	Enabled    bool              `gorm:"default:true" json:"enabled"`
}

type RouteCombo struct {
	ID       uint     `gorm:"primaryKey" json:"id"`
	Name     string   `gorm:"uniqueIndex;not null" json:"name"`
	Members  []string `gorm:"serializer:json;not null" json:"members"`
	IsActive bool     `gorm:"index" json:"is_active"`
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
	return &Store{db: db}, nil
}

func (s *Store) CreateProvider(p Provider) (Provider, error) {
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
	var p Provider
	if err := s.db.First(&p, id).Error; err != nil {
		return Provider{}, err
	}
	return p, nil
}

func (s *Store) UpdateProvider(id uint, p Provider) (Provider, error) {
	cur, err := s.GetProvider(id)
	if err != nil {
		return Provider{}, err
	}
	p.ID = cur.ID
	if err := p.Validate(); err != nil {
		return Provider{}, err
	}
	if err := s.db.Save(&p).Error; err != nil {
		return Provider{}, err
	}
	p.APIKeys = MaskKeys(p.APIKeys)
	return p, nil
}

func (s *Store) DeleteProvider(id uint) error { return s.db.Delete(&Provider{}, id).Error }

func (s *Store) ListCombos() ([]RouteCombo, error) {
	var out []RouteCombo
	if err := s.db.Order("name asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) SaveCombo(c RouteCombo) (RouteCombo, error) {
	if !nameRe.MatchString(c.Name) || len(c.Members) == 0 {
		return RouteCombo{}, fmt.Errorf("combo needs valid name and >=1 member")
	}
	c.ID = 0
	c.IsActive = false
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
	if !nameRe.MatchString(c.Name) || len(c.Members) == 0 {
		return RouteCombo{}, fmt.Errorf("combo needs valid name and >=1 member")
	}
	c.ID = cur.ID
	c.IsActive = cur.IsActive
	if err := s.db.Save(&c).Error; err != nil {
		return RouteCombo{}, err
	}
	return c, nil
}

func (s *Store) DeleteCombo(id uint) error { return s.db.Delete(&RouteCombo{}, id).Error }

func (s *Store) ActivateCombo(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&RouteCombo{}).Where("1 = 1").Update("is_active", false).Error; err != nil {
			return err
		}
		return tx.Model(&RouteCombo{}).Where("id = ?", id).Update("is_active", true).Error
	})
}

func (s *Store) ActiveCombo() (RouteCombo, error) {
	var c RouteCombo
	if err := s.db.Where("is_active = ?", true).First(&c).Error; err != nil {
		return RouteCombo{}, err
	}
	return c, nil
}
