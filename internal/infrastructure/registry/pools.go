package registry

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// MarkPoolTest records the outcome of a pool probe.
func (s *Store) MarkPoolTest(ctx context.Context, id uint, ok bool, lastErr string) error {
	status := "active"
	if !ok {
		status = "error"
	}
	result := s.db.WithContext(ctx).Model(&ProxyPool{}).Where("id = ?", id).Updates(map[string]any{
		"test_status": status, "last_error": lastErr,
	})
	if result.Error != nil {
		return wrapStoreError("mark pool test", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("mark pool test %d: %w", id, ErrNotFound)
	}
	return nil
}

func (s *Store) CreatePool(ctx context.Context, p ProxyPool) (ProxyPool, error) {
	if err := p.Validate(); err != nil {
		return ProxyPool{}, err
	}
	p.ID = 0
	enabled := p.Enabled
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&p).Error; err != nil {
			return err
		}
		if !enabled {
			p.Enabled = false
			return tx.Model(&p).Update("enabled", false).Error
		}
		return nil
	}); err != nil {
		return ProxyPool{}, wrapStoreError("create pool", err)
	}
	return p, nil
}

func (s *Store) ListPools(ctx context.Context) ([]ProxyPool, error) {
	var out []ProxyPool
	if err := s.db.WithContext(ctx).Order("name asc").Find(&out).Error; err != nil {
		return nil, wrapStoreError("list pools", err)
	}
	return out, nil
}

func (s *Store) GetPool(ctx context.Context, id uint) (ProxyPool, error) {
	var p ProxyPool
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		return ProxyPool{}, wrapStoreError("get pool", err)
	}
	return p, nil
}

func (s *Store) UpdatePool(ctx context.Context, id uint, p ProxyPool) (ProxyPool, error) {
	if err := p.Validate(); err != nil {
		return ProxyPool{}, err
	}
	p.ID = id
	result := s.db.WithContext(ctx).Model(&ProxyPool{}).Where("id = ?", id).Updates(map[string]any{
		"name":         p.Name,
		"proxy_url":    p.ProxyURL,
		"no_proxy":     p.NoProxy,
		"strict_proxy": p.StrictProxy,
		"enabled":      p.Enabled,
	})
	if result.Error != nil {
		return ProxyPool{}, wrapStoreError("update pool", result.Error)
	}
	if result.RowsAffected == 0 {
		return ProxyPool{}, fmt.Errorf("update pool %d: %w", id, ErrNotFound)
	}
	return p, nil
}

// DeletePool removes a pool and resets every pin on it (custom providers
// and builtins) back to the global rotation in one transaction, so a
// failure can never leave a half-deleted pool with dangling pins.
func (s *Store) DeletePool(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Delete(&ProxyPool{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("delete pool %d: %w", id, ErrNotFound)
		}
		reset := map[string]any{"proxy_mode": ProxyModeGlobal, "proxy_pool_id": nil}
		if err := tx.Model(&Provider{}).Where("proxy_pool_id = ?", id).Updates(reset).Error; err != nil {
			return err
		}
		return tx.Model(&BuiltinProxy{}).Where("proxy_pool_id = ?", id).Updates(reset).Error
	})
	return wrapStoreError("delete pool", err)
}

// UnpinPool resets providers pinned to the given pool back to the global
// rotation. Kept for callers that reset pins without deleting the pool;
// DeletePool covers the delete path transactionally above.
func (s *Store) UnpinPool(ctx context.Context, poolID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Provider{}).Where("proxy_pool_id = ?", poolID).Updates(map[string]any{
			"proxy_mode": ProxyModeGlobal, "proxy_pool_id": nil,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&BuiltinProxy{}).Where("proxy_pool_id = ?", poolID).Updates(map[string]any{
			"proxy_mode": ProxyModeGlobal, "proxy_pool_id": nil,
		}).Error
	})
	return wrapStoreError("unpin pool", err)
}

// GetBuiltinProxy returns the stored relay selection for a builtin
// upstream. A missing row means global rotation (zero value, nil error).
func (s *Store) GetBuiltinProxy(ctx context.Context, name string) (BuiltinProxy, error) {
	var b BuiltinProxy
	if err := s.db.WithContext(ctx).First(&b, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BuiltinProxy{Name: name}, nil
		}
		return BuiltinProxy{}, wrapStoreError("get builtin proxy", err)
	}
	b.ProxyMode = NormalizeProxyMode(b.ProxyMode)
	if b.ProxyMode != ProxyModePool {
		b.ProxyPoolID = nil
	}
	return b, nil
}

// ListBuiltinProxies returns the relay selection for every known builtin,
// filling global defaults for unconfigured ones.
func (s *Store) ListBuiltinProxies(ctx context.Context) ([]BuiltinProxy, error) {
	out := make([]BuiltinProxy, 0, len(KnownBuiltins))
	for _, name := range KnownBuiltins {
		b, err := s.GetBuiltinProxy(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// SetBuiltinProxy stores the relay selection for a builtin upstream.
// Pool mode requires an existing pool; other modes clear the pin.
func (s *Store) SetBuiltinProxy(ctx context.Context, name, mode string, poolID *uint) (BuiltinProxy, error) {
	if !IsBuiltin(name) {
		return BuiltinProxy{}, fmt.Errorf("%w: unknown builtin provider %q", ErrInvalidArgument, name)
	}
	if err := validateProxyMode(mode); err != nil {
		return BuiltinProxy{}, err
	}
	mode = NormalizeProxyMode(mode)
	if mode != ProxyModePool {
		poolID = nil
	} else {
		if poolID == nil {
			return BuiltinProxy{}, fmt.Errorf("%w: proxy_pool_id is required when proxy_mode is pool", ErrInvalidArgument)
		}
	}
	b := BuiltinProxy{Name: name, ProxyMode: mode, ProxyPoolID: poolID}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureProxyPool(tx, mode, poolID); err != nil {
			return err
		}
		return tx.Save(&b).Error
	}); err != nil {
		return BuiltinProxy{}, wrapStoreError("set builtin proxy", err)
	}
	return b, nil
}
