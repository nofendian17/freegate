package registry

import (
	"context"
	"database/sql"
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
)

const (
	maxOpenConnections = 16
	maxIdleConnections = 8
	connectionLifetime = 30 * time.Minute
	connectionIdleTime = 5 * time.Minute
)

type Store struct {
	db          *gorm.DB
	sqlDB       *sql.DB
	usage       *clientKeyUsageRecorder
	verifyCache *clientKeyVerifyCache
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
	if isMemoryDB(path) {
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
	s := &Store{db: db, sqlDB: sqlDB, verifyCache: newClientKeyVerifyCache()}
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
	pragmas := "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_txlock=immediate"
	if !isMemoryDB(path) {
		pragmas += "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}
	return path + separator + pragmas
}

func isMemoryDB(path string) bool {
	if path == ":memory:" {
		return true
	}
	lower := strings.ToLower(path)
	return strings.Contains(lower, "mode=memory")
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
