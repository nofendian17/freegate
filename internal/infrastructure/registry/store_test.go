package registry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpen_SecuresDatabasePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "providers.db")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("database directory permissions = %o, want 700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("database file permissions = %o, want 600", got)
	}
}

func TestOpen_ConfiguresConnectionPool(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := s.sqlDB.Stats().MaxOpenConnections; got != maxOpenConnections {
		t.Fatalf("max open connections = %d, want %d", got, maxOpenConnections)
	}
}

func TestOpen_ParameterizedLogsDoNotExposeSecrets(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	provider := Provider{
		Name: "secret-provider", BaseURL: "https://provider.test/v1",
		APIKeys: []string{"sk-live-super-secret"}, RefreshSec: 60, Enabled: true,
	}
	if _, err := s.CreateProvider(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if _, err := s.CreateProvider(t.Context(), provider); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate provider error = %v, want ErrConflict", err)
	}
	if strings.Contains(logs.String(), "sk-live-super-secret") {
		t.Fatalf("database log exposed provider secret: %s", logs.String())
	}
}

func TestStore_PropagatesContextCancellation(t *testing.T) {
	s, err := Open(t.Context(), "file:cancel?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ListProviders(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListProviders error = %v, want context.Canceled", err)
	}
}
