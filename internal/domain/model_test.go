package domain

import (
	"testing"
)

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
