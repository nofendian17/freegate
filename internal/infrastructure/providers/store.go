package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	appvalidation "freegate/internal/validation"
)

var (
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotFound        = errors.New("record not found")
	ErrConflict        = errors.New("record already exists")
	ErrStoreClosed     = errors.New("store is closed")
)

const (
	maxOpenConnections = 4
	maxIdleConnections = 4
	connectionLifetime = 30 * time.Minute
	connectionIdleTime = 5 * time.Minute
	sqlitePragmas      = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	resourceNameRules  = "required,resource_name"
	proxyModeRules     = "proxy_mode"
)

type Provider struct {
	ID      uint              `gorm:"primaryKey" json:"id"`
	Name    string            `gorm:"uniqueIndex;not null" json:"name" validate:"required,resource_name"`
	BaseURL string            `gorm:"not null" json:"base_url" validate:"required,http_url,max=2048"`
	APIKeys []string          `gorm:"serializer:json;not null" json:"-" validate:"omitempty,max=32,dive,required,max=4096"`
	Headers map[string]string `gorm:"serializer:json" json:"headers,omitempty" validate:"omitempty,http_headers,max=64"`
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
	Models     []string `gorm:"serializer:json" json:"models" validate:"omitempty,max=64,dive,max=256"`
	RefreshSec int      `gorm:"default:60" json:"refresh_sec" validate:"min=10,max=3600"`
	// Priority controls list ordering only; runtime order comes
	// solely from combo tiers.
	Priority int  `json:"priority"`
	Enabled  bool `gorm:"default:true" json:"enabled"`
	// ProxyMode selects edge-relay behavior for this provider:
	// "" (zero value) follows the global pool rotation, "direct"
	// skips all relays, "pool" pins to ProxyPoolID. Existing rows
	// default to global, preserving current behavior.
	ProxyMode string `json:"proxy_mode,omitempty" validate:"proxy_mode"`
	// ProxyPoolID pins the provider to one pool when ProxyMode is
	// "pool". Nil otherwise (cleared on save for other modes).
	ProxyPoolID *uint `json:"proxy_pool_id,omitempty" validate:"omitempty,min=1"`
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
	Provider string `json:"provider" validate:"required,max=128"`
	Model    string `json:"model,omitempty" validate:"omitempty,max=256"`
}

var KnownBuiltins = []string{"opencode", "kilo", "llm7"}

var knownBuiltin = map[string]bool{"opencode": true, "kilo": true, "llm7": true}

func IsBuiltin(name string) bool { return knownBuiltin[name] }

func validTierProvider(p string) bool {
	if knownBuiltin[p] {
		return true
	}
	if strings.HasPrefix(p, "custom:") {
		return validateResourceName(strings.TrimPrefix(p, "custom:")) == nil
	}
	return false
}

func validateTiers(tiers []ComboTier) error {
	if err := appvalidation.Field(tiers, "required,min=1,max=32,dive"); err != nil {
		return fmt.Errorf("%w: invalid combo tiers: %w", ErrInvalidArgument, err)
	}
	for i, tr := range tiers {
		if !validTierProvider(strings.TrimSpace(tr.Provider)) {
			return fmt.Errorf("%w: tier %d: unknown provider %q", ErrInvalidArgument, i+1, tr.Provider)
		}
	}
	return nil
}

type RouteCombo struct {
	ID    uint        `gorm:"primaryKey" json:"id"`
	Name  string      `gorm:"uniqueIndex;not null" json:"name" validate:"required,resource_name"`
	Tiers []ComboTier `gorm:"serializer:json" json:"tiers" validate:"required,min=1,max=32,dive"`
}

type ProxyPool struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"uniqueIndex;not null" json:"name" validate:"required,resource_name"`
	ProxyURL    string `gorm:"not null" json:"proxy_url" validate:"required,http_url,max=2048"`
	NoProxy     string `json:"no_proxy,omitempty" validate:"omitempty,max=2048"`
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

func (p *ProxyPool) Validate() error {
	if err := appvalidation.Struct(p); err != nil {
		return fmt.Errorf("%w: invalid proxy pool: %w", ErrInvalidArgument, err)
	}
	return nil
}

type legacyComboRow struct {
	ID      uint
	Tiers   []ComboTier `gorm:"serializer:json"`
	Members []string    `gorm:"serializer:json"`
}

func (p *Provider) Validate() error {
	if err := appvalidation.Struct(p); err != nil {
		return fmt.Errorf("%w: invalid provider: %w", ErrInvalidArgument, err)
	}
	if p.Enabled {
		n := 0
		for _, k := range p.APIKeys {
			if strings.TrimSpace(k) != "" {
				n++
			}
		}
		if n == 0 {
			return fmt.Errorf("%w: enabled provider needs at least one api key", ErrInvalidArgument)
		}
	}
	if NormalizeProxyMode(p.ProxyMode) == ProxyModePool && p.ProxyPoolID == nil {
		return fmt.Errorf("%w: proxy_pool_id is required when proxy_mode is pool", ErrInvalidArgument)
	}
	return nil
}

func validateResourceName(name string) error {
	if err := appvalidation.Field(name, resourceNameRules); err != nil {
		return fmt.Errorf("%w: invalid resource name: %w", ErrInvalidArgument, err)
	}
	return nil
}

func validateProxyMode(mode string) error {
	if err := appvalidation.Field(mode, proxyModeRules); err != nil {
		return fmt.Errorf("%w: invalid proxy mode: %w", ErrInvalidArgument, err)
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

type Store struct {
	db    *gorm.DB
	sqlDB *sql.DB
	usage *clientKeyUsageRecorder
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = "./data/providers.db"
	}
	if err := prepareSQLiteFile(path); err != nil {
		return nil, err
	}

	gormLogger := logger.NewSlogLogger(slog.Default(), logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
	})
	db, err := gorm.Open(sqlite.Open(sqliteDSN(path)), &gorm.Config{
		Logger:         gormLogger,
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open providers database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("access providers database: %w", err), closeSQLDB(db))
	}
	maxOpen, maxIdle := maxOpenConnections, maxIdleConnections
	if path == ":memory:" {
		maxOpen, maxIdle = 1, 1
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(connectionLifetime)
	sqlDB.SetConnMaxIdleTime(connectionIdleTime)
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping providers database: %w", err), closeSQLDB(db))
	}
	if err := db.WithContext(ctx).AutoMigrate(&Provider{}, &RouteCombo{}, &ProxyPool{}, &BuiltinProxy{}, &ClientKey{}); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate providers database: %w", err), closeSQLDB(db))
	}
	storeLogger := slog.Default()
	s := &Store{db: db, sqlDB: sqlDB}
	if err := s.migrateMembersToTiers(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate combo members to tiers: %w", err), s.closeSQL())
	}
	s.usage = newClientKeyUsageRecorder(db, storeLogger)
	return s, nil
}

func prepareSQLiteFile(path string) error {
	if strings.HasPrefix(path, "file:") || path == ":memory:" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure database directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close database file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure database file: %w", err)
	}
	return nil
}

func sqliteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + sqlitePragmas
}

func closeSQLDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("access database for cleanup: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("close database after initialization failure: %w", err)
	}
	return nil
}

func wrapStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict), errors.Is(err, ErrStoreClosed):
		return fmt.Errorf("%s: %w", operation, err)
	case errors.Is(err, gorm.ErrRecordNotFound):
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return fmt.Errorf("%s: %w", operation, ErrConflict)
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func (s *Store) backfillNullTiers(ctx context.Context) error {
	return s.db.WithContext(ctx).Table("route_combos").Where("tiers IS NULL").Update("tiers", "[]").Error
}

func (s *Store) migrateMembersToTiers(ctx context.Context) error {
	if !s.db.WithContext(ctx).Migrator().HasColumn(&RouteCombo{}, "members") {
		return s.backfillNullTiers(ctx)
	}
	var rows []legacyComboRow
	if err := s.db.WithContext(ctx).Table("route_combos").Find(&rows).Error; err != nil {
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
		if err := s.db.WithContext(ctx).Table("route_combos").Where("id = ?", c.ID).Update("tiers", string(raw)).Error; err != nil {
			return err
		}
	}
	return s.backfillNullTiers(ctx)
}

func (s *Store) Close() error {
	return s.closeSQL()
}

func (s *Store) closeSQL() error {
	var usageErr error
	if s.usage != nil {
		usageErr = s.usage.Close()
	}
	if s.sqlDB == nil {
		return usageErr
	}
	dbErr := s.sqlDB.Close()
	if dbErr != nil {
		dbErr = fmt.Errorf("close providers database: %w", dbErr)
	}
	return errors.Join(usageErr, dbErr)
}

func (s *Store) PingContext(ctx context.Context) error {
	if s.sqlDB == nil {
		return ErrStoreClosed
	}
	if err := s.sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping providers database: %w", err)
	}
	return nil
}

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
