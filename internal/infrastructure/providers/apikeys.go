package providers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ClientKey is a DB-managed API key for /v1/* clients. The raw secret is
// stored reversibly (same as upstream provider API keys in this DB) so it
// can be shown again later via RevealClientKey. List/get/update responses
// never carry the secret — only the explicit reveal path returns it, and
// that route is admin-only like the rest of /api/*.
// Config API_KEY entries keep working alongside these (superset).
type ClientKey struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Name      string     `gorm:"uniqueIndex;not null" json:"name"`
	KeyHash   string     `gorm:"uniqueIndex;not null" json:"-"`
	Plaintext string     `json:"-"`
	Prefix    string     `json:"prefix"`
	Enabled   bool       `gorm:"default:true" json:"enabled"`
	UseCount  int64      `json:"use_count"`
	LastUsed  *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

const clientKeyPrefix = "fg_"

// GenerateClientKey creates a random raw key ("fg_" + 32 hex chars) with
// its hash and display prefix. 128 bits of entropy: SHA-256 storage is
// the standard practice for such high-entropy API keys.
func GenerateClientKey() (raw, hash, prefix string, err error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", "", fmt.Errorf("generate key: %w", err)
	}
	raw = clientKeyPrefix + hex.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), raw[:len(clientKeyPrefix)+8], nil
}

func hashClientKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateClientKey stores a new client key and returns the row plus the raw
// key. The secret stays retrievable via RevealClientKey.
func (s *Store) CreateClientKey(name string) (ClientKey, string, error) {
	if !nameRe.MatchString(name) {
		return ClientKey{}, "", fmt.Errorf("name must match ^[a-z0-9-]{1,64}$")
	}
	raw, hash, prefix, err := GenerateClientKey()
	if err != nil {
		return ClientKey{}, "", err
	}
	row := ClientKey{Name: name, KeyHash: hash, Plaintext: raw, Prefix: prefix, Enabled: true}
	// GORM replaces false with the schema default on insert; Enabled is
	// true here so a plain Create suffices.
	if err := s.db.Create(&row).Error; err != nil {
		return ClientKey{}, "", err
	}
	return row, raw, nil
}

// ListClientKeys returns all client keys. Secrets never leave via this
// path: plaintext and hash are blanked on every read (list/get/update).
// Use RevealClientKey for the explicit per-key reveal.
func (s *Store) ListClientKeys() ([]ClientKey, error) {
	var out []ClientKey
	if err := s.db.Order("name asc").Find(&out).Error; err != nil {
		return nil, err
	}
	for i := range out {
		out[i].KeyHash = ""
		out[i].Plaintext = ""
	}
	return out, nil
}

// GetClientKey returns one client key by id (secret excluded).
func (s *Store) GetClientKey(id uint) (ClientKey, error) {
	var row ClientKey
	if err := s.db.First(&row, id).Error; err != nil {
		return ClientKey{}, err
	}
	row.KeyHash = ""
	row.Plaintext = ""
	return row, nil
}

// RevealClientKey returns the raw secret for one key. The only read path
// that exposes it; the admin route guarding this is admin-only. Keys
// created before the secret was stored are unrecoverable — rotate them.
func (s *Store) RevealClientKey(id uint) (string, error) {
	var row ClientKey
	if err := s.db.First(&row, id).Error; err != nil {
		return "", err
	}
	if row.Plaintext == "" {
		return "", fmt.Errorf("key predates stored secrets — rotate it (delete + create)")
	}
	return row.Plaintext, nil
}

// getClientKeyRaw returns the row including the hash. Internal-only:
// UpdateClientKey needs the hash to persist the row unchanged.
func (s *Store) getClientKeyRaw(id uint) (ClientKey, error) {
	var row ClientKey
	if err := s.db.First(&row, id).Error; err != nil {
		return ClientKey{}, err
	}
	return row, nil
}

// UpdateClientKey renames or enables/disables a client key. The secret
// itself never changes (delete + create to rotate).
func (s *Store) UpdateClientKey(id uint, name string, enabled bool) (ClientKey, error) {
	if !nameRe.MatchString(name) {
		return ClientKey{}, fmt.Errorf("name must match ^[a-z0-9-]{1,64}$")
	}
	row, err := s.getClientKeyRaw(id)
	if err != nil {
		return ClientKey{}, err
	}
	row.Name = name
	row.Enabled = enabled
	if err := s.db.Save(&row).Error; err != nil {
		return ClientKey{}, err
	}
	row.KeyHash = ""
	row.Plaintext = ""
	return row, nil
}

// DeleteClientKey removes a client key immediately.
func (s *Store) DeleteClientKey(id uint) error { return s.db.Delete(&ClientKey{}, id).Error }

// VerifyClientKey reports whether raw is an enabled client key.
// Empty input never verifies. Lookup is by hash equality, which the DB
// answers without leaking timing beyond existence — a follow-up compare
// would add nothing, so a single existence + enabled check is enough.
func (s *Store) VerifyClientKey(raw string) bool {
	if raw == "" {
		return false
	}
	var row ClientKey
	if err := s.db.Select("enabled").Where("key_hash = ?", hashClientKey(raw)).First(&row).Error; err != nil {
		return false
	}
	return row.Enabled
}

// TouchClientKey records one use (counter + timestamp). Best-effort:
// errors are ignored so auth accounting can never fail a request.
func (s *Store) TouchClientKey(raw string) {
	now := time.Now()
	_ = s.db.Model(&ClientKey{}).Where("key_hash = ?", hashClientKey(raw)).Updates(map[string]any{
		"use_count": gorm.Expr("use_count + ?", 1), "last_used_at": now,
	}).Error
}
