package domain

import (
	"encoding/json"
	"testing"
)

func TestModelContextWindowJSON(t *testing.T) {
	// /v1/models clients (Claude Code etc.) read context_window to size
	// their context; the field must be present on the wire when known.
	in := Model{ID: "m", ContextWindow: 200000}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if n, ok := got["context_window"].(float64); !ok || n != 200000 {
		t.Fatalf("context_window on wire = %v (%T), want 200000", got["context_window"], got["context_window"])
	}

	// Unknown context must be omitted rather than serialized as 0, so
	// clients can distinguish "no data" from a zero/empty window.
	raw, err = json.Marshal(Model{ID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["context_window"]; ok {
		t.Fatalf("context_window should be omitted when unknown: %s", raw)
	}
}

func TestModelLimitJSON(t *testing.T) {
	// Nested limit mirrors OpenCode's /models shape: clients that choose
	// the native form get context/input/output in one object.
	in := Model{ID: "m", Limit: &ModelLimit{Context: 200000, Input: 160000, Output: 32000}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Limit *struct {
			Context int64 `json:"context"`
			Input   int64 `json:"input"`
			Output  int64 `json:"output"`
		} `json:"limit"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Limit == nil || got.Limit.Context != 200000 || got.Limit.Input != 160000 || got.Limit.Output != 32000 {
		t.Fatalf("limit on wire = %+v, want context/input/output set", got.Limit)
	}

	// Zero fields inside a present limit are omitted, not serialized as 0.
	raw, err = json.Marshal(Model{ID: "m", Limit: &ModelLimit{Context: 200000}})
	if err != nil {
		t.Fatal(err)
	}
	var sparse map[string]any
	if err := json.Unmarshal(raw, &sparse); err != nil {
		t.Fatal(err)
	}
	limit, ok := sparse["limit"].(map[string]any)
	if !ok {
		t.Fatalf("limit missing: %s", raw)
	}
	if _, ok := limit["input"]; ok {
		t.Errorf("zero input should be omitted: %s", raw)
	}
	if _, ok := limit["output"]; ok {
		t.Errorf("zero output should be omitted: %s", raw)
	}

	// Nil limit must be omitted so older catalog entries keep their shape.
	raw, err = json.Marshal(Model{ID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got.Limit = nil
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Limit != nil {
		t.Fatalf("limit should be omitted when unknown: %s", raw)
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
