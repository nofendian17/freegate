package registry

import (
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

// TestStore_ClientKeys verifies DB-managed API keys: create returns the
// raw secret, reveal returns it again later, verification accepts it,
// disabled keys fail, TouchClientKey records use, and rename/enable/delete
// work. List/get/update responses never carry secret material.
func TestStore_ClientKeys(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	row, raw, err := s.CreateClientKey(t.Context(), "client-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(raw) < len("fg_")+8 || row.Prefix == "" || row.Prefix != raw[:len(row.Prefix)] {
		t.Fatalf("prefix must match raw head: row=%+v raw=%q", row, raw)
	}
	if !verifyClientKey(t, s, raw) {
		t.Fatal("freshly created key must verify")
	}
	if verifyClientKey(t, s, "") || verifyClientKey(t, s, "bogus") {
		t.Fatal("unknown key must not verify")
	}
	list, err := s.ListClientKeys(t.Context())
	if err != nil || len(list) != 1 || list[0].KeyHash != "" || list[0].Plaintext != "" {
		t.Fatalf("list must hide secret material: %v %+v", err, list)
	}
	rev, err := s.RevealClientKey(t.Context(), row.ID)
	if err != nil || rev != raw {
		t.Fatalf("reveal must return the secret: %q %v", rev, err)
	}
	if _, err := s.RevealClientKey(t.Context(), 999999); err == nil {
		t.Fatal("reveal of missing key must fail")
	}
	if err := s.TouchClientKey(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	got := waitForUseCount(t, s, row.ID, 1)
	if got.UseCount != 1 || got.LastUsed == nil {
		t.Fatalf("touch must record use: %+v", got)
	}
	off, err := s.UpdateClientKey(t.Context(), row.ID, "client-1", false)
	if err != nil || off.Enabled {
		t.Fatalf("disable: %+v %v", off, err)
	}
	if verifyClientKey(t, s, raw) {
		t.Fatal("disabled key must not verify")
	}
	renamed, err := s.UpdateClientKey(t.Context(), row.ID, "client-2", true)
	if err != nil || renamed.Name != "client-2" {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if !verifyClientKey(t, s, raw) {
		t.Fatal("re-enabled key must verify")
	}
	if _, _, err := s.CreateClientKey(t.Context(), "bad name!"); err == nil {
		t.Fatal("expected invalid name to fail")
	}
	if err := s.DeleteClientKey(t.Context(), row.ID); err != nil {
		t.Fatal(err)
	}
	if verifyClientKey(t, s, raw) {
		t.Fatal("deleted key must not verify")
	}
}

func TestUpdateClientKey_PreservesConcurrentUsage(t *testing.T) {
	s, err := Open(t.Context(), "file:key-update?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	row, raw, err := s.CreateClientKey(t.Context(), "client")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Callback().Update().Before("gorm:update").Register("test:concurrent-touch", func(tx *gorm.DB) {
		if tx.Statement.Table == "client_keys" {
			tx.Exec("UPDATE client_keys SET use_count = use_count + 1 WHERE id = ?", row.ID)
		}
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpdateClientKey(t.Context(), row.ID, "renamed", true); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetClientKey(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UseCount != 1 {
		t.Fatalf("use count = %d, want 1; key metadata update overwrote usage", got.UseCount)
	}
	valid, err := s.VerifyClientKey(t.Context(), raw)
	if err != nil || !valid {
		t.Fatalf("renamed key must remain valid: valid=%v err=%v", valid, err)
	}
}

func TestStore_CloseDrainsClientKeyUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	row, raw, err := s.CreateClientKey(t.Context(), "client")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TouchClientKey(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.GetClientKey(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UseCount != 1 {
		t.Fatalf("use count after close = %d, want 1", got.UseCount)
	}
}

func verifyClientKey(t *testing.T, store *Store, raw string) bool {
	t.Helper()
	valid, err := store.VerifyClientKey(t.Context(), raw)
	if err != nil {
		t.Fatalf("verify client key: %v", err)
	}
	return valid
}

func waitForUseCount(t *testing.T, store *Store, id uint, want int64) ClientKey {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		row, err := store.GetClientKey(t.Context(), id)
		if err != nil {
			t.Fatalf("get client key: %v", err)
		}
		if row.UseCount == want {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("use count = %d, want %d", row.UseCount, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
