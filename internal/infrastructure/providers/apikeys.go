package providers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"
)

// ClientKey is a DB-managed API key for /v1/* clients. The raw secret is
// stored reversibly (same as upstream provider API keys in this DB) so it
// can be shown again later via RevealClientKey. List/get/update responses
// never carry the secret — only the explicit reveal path returns it, and
// that route is admin-only like the rest of /api/*.
type ClientKey struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Name      string     `gorm:"uniqueIndex;not null" json:"name" validate:"required,resource_name"`
	KeyHash   string     `gorm:"uniqueIndex;not null" json:"-"`
	Plaintext string     `json:"-"`
	Prefix    string     `json:"prefix"`
	Enabled   bool       `gorm:"default:true" json:"enabled"`
	UseCount  int64      `json:"use_count"`
	LastUsed  *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

const (
	clientKeyPrefix        = "fg_"
	clientKeyQueueSize     = 1024
	clientKeyBatchSize     = 64
	clientKeyBatchInterval = 100 * time.Millisecond
	clientKeyFlushTimeout  = 5 * time.Second
)

// GenerateClientKey creates a random raw key ("fg_" + 32 hex chars) with
// its hash and display prefix. 128 bits of entropy: SHA-256 storage is
// the standard practice for such high-entropy API keys.
func GenerateClientKey() (raw, hash, prefix string, err error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", "", fmt.Errorf("generate client key: %w", err)
	}
	raw = clientKeyPrefix + hex.EncodeToString(random[:])
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), raw[:len(clientKeyPrefix)+8], nil
}

func hashClientKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateClientKey stores a new client key and returns the row plus the raw
// key. The secret stays retrievable via RevealClientKey.
func (s *Store) CreateClientKey(ctx context.Context, name string) (ClientKey, string, error) {
	if err := validateResourceName(name); err != nil {
		return ClientKey{}, "", err
	}
	raw, hash, prefix, err := GenerateClientKey()
	if err != nil {
		return ClientKey{}, "", err
	}
	row := ClientKey{Name: name, KeyHash: hash, Plaintext: raw, Prefix: prefix, Enabled: true}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return ClientKey{}, "", wrapStoreError("create client key", err)
	}
	return row, raw, nil
}

// ListClientKeys returns all client keys. Secrets never leave via this
// path: plaintext and hash are blanked on every read (list/get/update).
// Use RevealClientKey for the explicit per-key reveal.
func (s *Store) ListClientKeys(ctx context.Context) ([]ClientKey, error) {
	var out []ClientKey
	if err := s.db.WithContext(ctx).
		Omit("key_hash", "plaintext").
		Order("name asc").
		Find(&out).Error; err != nil {
		return nil, wrapStoreError("list client keys", err)
	}
	for i := range out {
		out[i].KeyHash = ""
		out[i].Plaintext = ""
	}
	return out, nil
}

// GetClientKey returns one client key by id (secret excluded).
func (s *Store) GetClientKey(ctx context.Context, id uint) (ClientKey, error) {
	var row ClientKey
	if err := s.db.WithContext(ctx).
		Omit("key_hash", "plaintext").
		First(&row, id).Error; err != nil {
		return ClientKey{}, wrapStoreError("get client key", err)
	}
	row.KeyHash = ""
	row.Plaintext = ""
	return row, nil
}

// RevealClientKey returns the raw secret for one key. The only read path
// that exposes it; the admin route guarding this is admin-only. Keys
// created before the secret was stored are unrecoverable — rotate them.
func (s *Store) RevealClientKey(ctx context.Context, id uint) (string, error) {
	var row ClientKey
	if err := s.db.WithContext(ctx).
		Select("plaintext").
		First(&row, id).Error; err != nil {
		return "", wrapStoreError("reveal client key", err)
	}
	if row.Plaintext == "" {
		return "", fmt.Errorf("%w: key predates stored secrets; rotate it", ErrInvalidArgument)
	}
	return row.Plaintext, nil
}

// UpdateClientKey renames or enables/disables a client key. The secret
// itself never changes (delete + create to rotate).
func (s *Store) UpdateClientKey(ctx context.Context, id uint, name string, enabled bool) (ClientKey, error) {
	if err := validateResourceName(name); err != nil {
		return ClientKey{}, err
	}
	result := s.db.WithContext(ctx).
		Model(&ClientKey{}).
		Where("id = ?", id).
		Updates(map[string]any{"name": name, "enabled": enabled})
	if result.Error != nil {
		return ClientKey{}, wrapStoreError("update client key", result.Error)
	}
	if result.RowsAffected == 0 {
		return ClientKey{}, fmt.Errorf("update client key %d: %w", id, ErrNotFound)
	}
	return s.GetClientKey(ctx, id)
}

// DeleteClientKey removes a client key immediately.
func (s *Store) DeleteClientKey(ctx context.Context, id uint) error {
	result := s.db.WithContext(ctx).Delete(&ClientKey{}, id)
	if result.Error != nil {
		return wrapStoreError("delete client key", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete client key %d: %w", id, ErrNotFound)
	}
	return nil
}

// VerifyClientKey reports whether raw is an enabled client key.
func (s *Store) VerifyClientKey(ctx context.Context, raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	var row ClientKey
	err := s.db.WithContext(ctx).
		Select("enabled").
		Where("key_hash = ?", hashClientKey(raw)).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, wrapStoreError("verify client key", err)
	}
	return row.Enabled, nil
}

// TouchClientKey queues one usage update. The recorder batches writes and
// Store.Close drains the queue before closing the database.
func (s *Store) TouchClientKey(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	if s.usage == nil {
		return ErrStoreClosed
	}
	return s.usage.Enqueue(ctx, hashClientKey(raw))
}

type clientKeyUsage struct {
	count    int64
	lastUsed time.Time
}

type clientKeyUsageRecorder struct {
	db        *gorm.DB
	logger    *slog.Logger
	queue     chan string
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	mu        sync.RWMutex
	closed    bool
}

func newClientKeyUsageRecorder(db *gorm.DB, logger *slog.Logger) *clientKeyUsageRecorder {
	recorder := &clientKeyUsageRecorder{
		db:     db,
		logger: logger,
		queue:  make(chan string, clientKeyQueueSize),
		done:   make(chan struct{}),
	}
	go recorder.run()
	return recorder
}

func (r *clientKeyUsageRecorder) Enqueue(ctx context.Context, hash string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrStoreClosed
	}
	select {
	case r.queue <- hash:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *clientKeyUsageRecorder) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.queue)
		r.mu.Unlock()
		<-r.done
	})
	return r.closeErr
}

func (r *clientKeyUsageRecorder) run() {
	defer close(r.done)
	ticker := time.NewTicker(clientKeyBatchInterval)
	defer ticker.Stop()

	pending := make(map[string]clientKeyUsage)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), clientKeyFlushTimeout)
		defer cancel()
		err := r.flush(ctx, pending)
		clear(pending)
		return err
	}

	for {
		select {
		case hash, ok := <-r.queue:
			if !ok {
				r.closeErr = flush()
				return
			}
			usage := pending[hash]
			usage.count++
			usage.lastUsed = time.Now()
			pending[hash] = usage
			if len(pending) >= clientKeyBatchSize {
				if err := flush(); err != nil {
					r.logger.Error("failed to persist client key usage", "error", err)
				}
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				r.logger.Error("failed to persist client key usage", "error", err)
			}
		}
	}
}

func (r *clientKeyUsageRecorder) flush(ctx context.Context, pending map[string]clientKeyUsage) error {
	var errs []error
	for hash, usage := range pending {
		result := r.db.WithContext(ctx).
			Model(&ClientKey{}).
			Where("key_hash = ?", hash).
			Updates(map[string]any{
				"use_count":    gorm.Expr("use_count + ?", usage.count),
				"last_used_at": usage.lastUsed,
			})
		if result.Error != nil {
			errs = append(errs, wrapStoreError("persist client key usage", result.Error))
		}
	}
	return errors.Join(errs...)
}
