package registry

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

func (s *Store) ListCombos(ctx context.Context) ([]RouteCombo, error) {
	var out []RouteCombo
	if err := s.db.WithContext(ctx).Order("name asc").Find(&out).Error; err != nil {
		return nil, wrapStoreError("list combos", err)
	}
	return out, nil
}

func (s *Store) SaveCombo(ctx context.Context, c RouteCombo) (RouteCombo, error) {
	if err := validateResourceName(c.Name); err != nil {
		return RouteCombo{}, err
	}
	for i := range c.Tiers {
		c.Tiers[i].Provider = strings.TrimSpace(c.Tiers[i].Provider)
	}
	if err := validateTiers(c.Tiers); err != nil {
		return RouteCombo{}, err
	}
	c.ID = 0
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.checkTiersExist(ctx, tx, c.Tiers); err != nil {
			return err
		}
		return tx.Create(&c).Error
	}); err != nil {
		return RouteCombo{}, wrapStoreError("save combo", err)
	}
	return c, nil
}

func (s *Store) UpdateCombo(ctx context.Context, id uint, c RouteCombo) (RouteCombo, error) {
	if err := validateResourceName(c.Name); err != nil {
		return RouteCombo{}, err
	}
	for i := range c.Tiers {
		c.Tiers[i].Provider = strings.TrimSpace(c.Tiers[i].Provider)
	}
	if err := validateTiers(c.Tiers); err != nil {
		return RouteCombo{}, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current RouteCombo
		if err := tx.First(&current, id).Error; err != nil {
			return err
		}
		if err := s.checkTiersExist(ctx, tx, c.Tiers); err != nil {
			return err
		}
		c.ID = current.ID
		return tx.Save(&c).Error
	}); err != nil {
		return RouteCombo{}, wrapStoreError("update combo", err)
	}
	return c, nil
}

func (s *Store) DeleteCombo(ctx context.Context, id uint) error {
	result := s.db.WithContext(ctx).Delete(&RouteCombo{}, id)
	if result.Error != nil {
		return wrapStoreError("delete combo", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete combo %d: %w", id, ErrNotFound)
	}
	return nil
}
