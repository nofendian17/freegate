package registry

import (
	"errors"
	"testing"
)

func TestProvider_Validate_RejectsBadName(t *testing.T) {
	s, err := Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CreateProvider(t.Context(), Provider{Name: "Bad Name!", BaseURL: "https://x.test/v1", APIKeys: []string{"k"}}); err == nil {
		t.Fatal("expected validation error for bad name")
	}
}

func TestStore_DomainErrors(t *testing.T) {
	s, err := Open(t.Context(), "file:domain-errors?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.GetClientKey(t.Context(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing client key error = %v, want ErrNotFound", err)
	}
	if _, _, err := s.CreateClientKey(t.Context(), "duplicate"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateClientKey(t.Context(), "duplicate"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate client key error = %v, want ErrConflict", err)
	}
}
