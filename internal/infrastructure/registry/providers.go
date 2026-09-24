package registry

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

func (s *Store) CreateProvider(ctx context.Context, p Provider) (Provider, error) {
	if err := validateProxyMode(p.ProxyMode); err != nil {
		return Provider{}, err
	}
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
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureProxyPool(tx, p.ProxyMode, p.ProxyPoolID); err != nil {
			return err
		}
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
		return Provider{}, wrapStoreError("create provider", err)
	}
	p.APIKeys = MaskKeys(p.APIKeys)
	return p, nil
}

func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	var out []Provider
	if err := s.db.WithContext(ctx).Order("priority asc, name asc").Find(&out).Error; err != nil {
		return nil, wrapStoreError("list providers", err)
	}
	for i := range out {
		out[i].APIKeys = MaskKeys(out[i].APIKeys)
	}
	return out, nil
}

func (s *Store) GetProvider(ctx context.Context, id uint) (Provider, error) {
	p, err := s.GetProviderRaw(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	p.APIKeys = MaskKeys(p.APIKeys)
	return p, nil
}

// GetProviderByName returns the provider with raw (unmasked) API keys.
func (s *Store) GetProviderByName(ctx context.Context, name string) (Provider, error) {
	var p Provider
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&p).Error; err != nil {
		return Provider{}, wrapStoreError("get provider by name", err)
	}
	return p, nil
}

// GetProviderRaw returns the provider with raw (unmasked) API keys.
// Internal-only: for the manager/dialer. API responses must use GetProvider.
func (s *Store) GetProviderRaw(ctx context.Context, id uint) (Provider, error) {
	var p Provider
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		return Provider{}, wrapStoreError("get provider", err)
	}
	return p, nil
}

func (s *Store) UpdateProvider(ctx context.Context, id uint, p Provider) (Provider, error) {
	if err := validateProxyMode(p.ProxyMode); err != nil {
		return Provider{}, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if err := ensureProxyPool(tx, p.ProxyMode, p.ProxyPoolID); err != nil {
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
		return Provider{}, wrapStoreError("update provider", err)
	}
	p.APIKeys = MaskKeys(p.APIKeys)
	return p, nil
}

func (s *Store) DeleteProvider(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	return wrapStoreError("delete provider", err)
}

func ensureProxyPool(tx *gorm.DB, mode string, poolID *uint) error {
	if NormalizeProxyMode(mode) != ProxyModePool {
		return nil
	}
	if poolID == nil {
		return fmt.Errorf("%w: proxy_pool_id is required when proxy_mode is pool", ErrInvalidArgument)
	}
	var count int64
	if err := tx.Model(&ProxyPool{}).Where("id = ?", *poolID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("%w: proxy pool %d: %w", ErrInvalidArgument, *poolID, ErrNotFound)
	}
	return nil
}

func (s *Store) checkTiersExist(ctx context.Context, tx *gorm.DB, tiers []ComboTier) error {
	for i, tr := range tiers {
		p := strings.TrimSpace(tr.Provider)
		if IsBuiltin(p) {
			continue
		}
		if !strings.HasPrefix(p, "custom:") {
			continue
		}
		row, err := getProviderByName(ctx, tx, strings.TrimPrefix(p, "custom:"))
		if err != nil {
			return fmt.Errorf("tier %d: failed to look up provider %q: %w", i+1, tr.Provider, err)
		}
		if !row.Enabled {
			return fmt.Errorf("%w: tier %d: provider %q is disabled", ErrInvalidArgument, i+1, tr.Provider)
		}
	}
	return nil
}

func getProviderByName(ctx context.Context, db *gorm.DB, name string) (Provider, error) {
	var provider Provider
	if err := db.WithContext(ctx).Where("name = ?", name).First(&provider).Error; err != nil {
		return Provider{}, err
	}
	return provider, nil
}
