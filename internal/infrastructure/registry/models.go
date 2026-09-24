package registry

import (
	"errors"
	"fmt"
	"strings"

	appvalidation "freegate/internal/validation"
)

var (
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotFound        = errors.New("record not found")
	ErrConflict        = errors.New("record already exists")
	ErrStoreClosed     = errors.New("store is closed")
)

const (
	resourceNameRules = "required,resource_name"
	proxyModeRules    = "proxy_mode"
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

func (p *ProxyPool) Validate() error {
	if err := appvalidation.Struct(p); err != nil {
		return fmt.Errorf("%w: invalid proxy pool: %w", ErrInvalidArgument, err)
	}
	return nil
}

// BuiltinProxy stores the edge-relay selection of a builtin upstream
// (opencode, kilo, llm7). Absent row means global rotation. Same mode
// semantics as Provider.ProxyMode/ProxyPoolID.
type BuiltinProxy struct {
	Name        string `gorm:"primaryKey" json:"name"`
	ProxyMode   string `json:"proxy_mode,omitempty"`
	ProxyPoolID *uint  `json:"proxy_pool_id,omitempty"`
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
