package model

import (
	"testing"
)

func TestNewError(t *testing.T) {
	e := NewError("test_type", "test message")
	if e.Error.Type != "test_type" {
		t.Fatalf("expected test_type, got %s", e.Error.Type)
	}
	if e.Error.Message != "test message" {
		t.Fatalf("expected 'test message', got %s", e.Error.Message)
	}
}

func TestDomainErrors(t *testing.T) {
	if ErrModelNotFound.Error() != "model not found" {
		t.Fatalf("unexpected error message: %s", ErrModelNotFound)
	}
	if ErrEmptyRequestBody.Error() != "empty request body" {
		t.Fatalf("unexpected error message: %s", ErrEmptyRequestBody)
	}
	if ErrBodyTooLarge.Error() != "request body too large" {
		t.Fatalf("unexpected error message: %s", ErrBodyTooLarge)
	}
}
