package application

import (
	"sync"
	"testing"

	"freegate/internal/domain"
)

// mockRegistry is a test double for RouterRegistry.
type mockRegistry struct {
	models []domain.Model
	ready  bool
}

func (m *mockRegistry) AllModels() []domain.Model { return m.models }
func (m *mockRegistry) IsReady() bool              { return m.ready }

func TestNewModelService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		routers []RouterRegistry
		wantLen int
		wantOK  bool
	}{
		{
			name:    "no routers",
			routers: nil,
			wantLen: 0,
			wantOK:  false,
		},
		{
			name: "single router not ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: false, models: []domain.Model{{ID: "a"}}},
			},
			wantLen: 1,
			wantOK:  false,
		},
		{
			name: "single router ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: true, models: []domain.Model{{ID: "a"}}},
			},
			wantLen: 1,
			wantOK:  true,
		},
		{
			name: "multiple routers",
			routers: []RouterRegistry{
				&mockRegistry{ready: true, models: []domain.Model{{ID: "a"}}},
				&mockRegistry{ready: false, models: []domain.Model{{ID: "b"}}},
			},
			wantLen: 2,
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var svc *ModelService
			if tt.routers == nil {
				svc = NewModelService()
			} else {
				svc = NewModelService(tt.routers...)
			}
			if svc == nil {
				t.Fatal("NewModelService returned nil")
			}
			if got := len(svc.AllModels()); got != tt.wantLen {
				t.Errorf("AllModels() len = %d, want %d", got, tt.wantLen)
			}
			if got := svc.IsReady(); got != tt.wantOK {
				t.Errorf("IsReady() = %v, want %v", got, tt.wantOK)
			}
		})
	}
}

func TestModelService_AddRouter(t *testing.T) {
	t.Parallel()

	t.Run("append to empty service", func(t *testing.T) {
		t.Parallel()
		svc := NewModelService()
		if len(svc.AllModels()) != 0 {
			t.Fatalf("expected empty before AddRouter")
		}
		if svc.IsReady() {
			t.Fatalf("expected not ready before AddRouter")
		}

		r := &mockRegistry{ready: true, models: []domain.Model{{ID: "m1"}}}
		svc.AddRouter(r)

		if got := len(svc.AllModels()); got != 1 {
			t.Errorf("AllModels() after AddRouter = %d, want 1", got)
		}
		if !svc.IsReady() {
			t.Errorf("IsReady() after AddRouter = false, want true")
		}
	})

	t.Run("append multiple routers sequentially", func(t *testing.T) {
		t.Parallel()
		svc := NewModelService()
		r1 := &mockRegistry{ready: false, models: []domain.Model{{ID: "a"}}}
		r2 := &mockRegistry{ready: true, models: []domain.Model{{ID: "b"}}}
		r3 := &mockRegistry{ready: false, models: []domain.Model{{ID: "c"}}}

		svc.AddRouter(r1)
		if svc.IsReady() {
			t.Errorf("IsReady() after first not-ready router = true, want false")
		}
		svc.AddRouter(r2)
		if !svc.IsReady() {
			t.Errorf("IsReady() after second ready router = false, want true")
		}
		svc.AddRouter(r3)

		got := svc.AllModels()
		if len(got) != 3 {
			t.Fatalf("AllModels() len = %d, want 3", len(got))
		}
		ids := map[string]bool{}
		for _, m := range got {
			ids[m.ID] = true
		}
		for _, want := range []string{"a", "b", "c"} {
			if !ids[want] {
				t.Errorf("AllModels() missing %q", want)
			}
		}
	})

	t.Run("AddRouter deduplication visible via AllModels", func(t *testing.T) {
		t.Parallel()
		r1 := &mockRegistry{models: []domain.Model{{ID: "dup", OwnedBy: "first"}}}
		r2 := &mockRegistry{models: []domain.Model{{ID: "dup", OwnedBy: "second"}}}
		svc := NewModelService(r1)
		svc.AddRouter(r2)

		got := svc.AllModels()
		if len(got) != 1 {
			t.Fatalf("AllModels() len = %d, want 1 (deduplicated)", len(got))
		}
		if got[0].OwnedBy != "first" {
			t.Errorf("OwnedBy = %q, want %q (first occurrence wins)", got[0].OwnedBy, "first")
		}
	})
}

func TestModelService_AllModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		routers []RouterRegistry
		wantIDs []string
		// optional check for first-wins ownership when IDs overlap
		wantOwnedBy map[string]string
	}{
		{
			name:      "no routers returns empty",
			routers:   nil,
			wantIDs:   nil,
		},
		{
			name: "single router single model",
			routers: []RouterRegistry{
				&mockRegistry{models: []domain.Model{{ID: "m1", OwnedBy: "openai"}}},
			},
			wantIDs: []string{"m1"},
		},
		{
			name: "single router multiple models preserves order",
			routers: []RouterRegistry{
				&mockRegistry{models: []domain.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}},
			},
			wantIDs: []string{"a", "b", "c"},
		},
		{
			name: "single router empty returns empty",
			routers: []RouterRegistry{
				&mockRegistry{models: nil},
			},
			wantIDs: nil,
		},
		{
			name: "multiple routers no overlap union",
			routers: []RouterRegistry{
				&mockRegistry{models: []domain.Model{{ID: "a"}, {ID: "b"}}},
				&mockRegistry{models: []domain.Model{{ID: "c"}, {ID: "d"}}},
			},
			wantIDs: []string{"a", "b", "c", "d"},
		},
		{
			name: "overlap deduplicated first wins",
			routers: []RouterRegistry{
				&mockRegistry{models: []domain.Model{{ID: "shared", OwnedBy: "first"}, {ID: "a"}}},
				&mockRegistry{models: []domain.Model{{ID: "shared", OwnedBy: "second"}, {ID: "b"}}},
			},
			wantIDs: []string{"shared", "a", "b"},
			wantOwnedBy: map[string]string{"shared": "first"},
		},
		{
			name: "overlap across three routers deduplicated",
			routers: []RouterRegistry{
				&mockRegistry{models: []domain.Model{{ID: "x", Provider: "p1"}}},
				&mockRegistry{models: []domain.Model{{ID: "x", Provider: "p2"}}},
				&mockRegistry{models: []domain.Model{{ID: "x", Provider: "p3"}}},
			},
			wantIDs: []string{"x"},
			wantOwnedBy: nil, // provider check below is not keyed; just verify len
		},
		{
			name: "duplicate inside single router deduplicated",
			routers: []RouterRegistry{
				&mockRegistry{models: []domain.Model{{ID: "dup"}, {ID: "dup"}, {ID: "other"}}},
			},
			wantIDs: []string{"dup", "other"},
		},
		{
			name: "empty router mixed with non-empty",
			routers: []RouterRegistry{
				&mockRegistry{models: nil},
				&mockRegistry{models: []domain.Model{{ID: "a"}}},
				&mockRegistry{models: []domain.Model{}},
				&mockRegistry{models: []domain.Model{{ID: "b"}}},
			},
			wantIDs: []string{"a", "b"},
		},
		{
			name: "order preserves first occurrence across routers",
			routers: []RouterRegistry{
				&mockRegistry{models: []domain.Model{{ID: "b"}, {ID: "a"}}},
				&mockRegistry{models: []domain.Model{{ID: "a"}, {ID: "c"}}},
			},
			wantIDs: []string{"b", "a", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewModelService(tt.routers...)
			got := svc.AllModels()

			if len(got) != len(tt.wantIDs) {
				t.Fatalf("AllModels() len = %d, want %d; got IDs %v, want %v", len(got), len(tt.wantIDs), ids(got), tt.wantIDs)
			}
			for i, wantID := range tt.wantIDs {
				if got[i].ID != wantID {
					t.Errorf("AllModels()[%d].ID = %q, want %q (full: %v)", i, got[i].ID, wantID, ids(got))
				}
			}
			if tt.wantOwnedBy != nil {
				for _, m := range got {
					if want, ok := tt.wantOwnedBy[m.ID]; ok && m.OwnedBy != want {
						t.Errorf("model %q OwnedBy = %q, want %q", m.ID, m.OwnedBy, want)
					}
				}
			}
			// Verify that mutating the returned slice does not affect next call's deduplication
			// (returned slice is a copy; service state is not aliased).
			if len(got) > 0 {
				origFirst := got[0].ID
				got[0].ID = "mutated"
				got2 := svc.AllModels()
				if got2[0].ID != origFirst {
					t.Errorf("AllModels() returned slice aliases internal state: after mutation got %q, want %q", got2[0].ID, origFirst)
				}
			}
		})
	}
}

func TestModelService_IsReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		routers []RouterRegistry
		want    bool
	}{
		{
			name:    "no routers not ready",
			routers: nil,
			want:    false,
		},
		{
			name: "single router not ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: false},
			},
			want: false,
		},
		{
			name: "single router ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: true},
			},
			want: true,
		},
		{
			name: "multiple routers none ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: false},
				&mockRegistry{ready: false},
				&mockRegistry{ready: false},
			},
			want: false,
		},
		{
			name: "multiple routers one ready in middle",
			routers: []RouterRegistry{
				&mockRegistry{ready: false},
				&mockRegistry{ready: true},
				&mockRegistry{ready: false},
			},
			want: true,
		},
		{
			name: "multiple routers first ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: true},
				&mockRegistry{ready: false},
			},
			want: true,
		},
		{
			name: "multiple routers last ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: false},
				&mockRegistry{ready: false},
				&mockRegistry{ready: true},
			},
			want: true,
		},
		{
			name: "all routers ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: true},
				&mockRegistry{ready: true},
			},
			want: true,
		},
		{
			name: "empty models but ready true still ready",
			routers: []RouterRegistry{
				&mockRegistry{ready: true, models: nil},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewModelService(tt.routers...)
			if got := svc.IsReady(); got != tt.want {
				t.Errorf("IsReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelService_ConcurrentAccess(t *testing.T) {
	svc := NewModelService(
		&mockRegistry{ready: true, models: []domain.Model{{ID: "a"}, {ID: "b"}}},
		&mockRegistry{ready: false, models: []domain.Model{{ID: "c"}}},
	)

	var wg sync.WaitGroup
	// Concurrent readers: AllModels and IsReady
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.AllModels()
			_ = svc.IsReady()
		}()
	}
	// Concurrent writers: AddRouter
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r := &mockRegistry{ready: n%2 == 0, models: []domain.Model{{ID: "concurrent"}}}
			svc.AddRouter(r)
		}(i)
	}
	wg.Wait()

	// After concurrent ops, service must still be consistent.
	got := svc.AllModels()
	if len(got) == 0 {
		t.Fatal("AllModels() after concurrent access returned empty, want at least 3")
	}
	// Deduplication must hold: "concurrent" appears at most once.
	seen := map[string]int{}
	for _, m := range got {
		seen[m.ID]++
	}
	if seen["concurrent"] > 1 {
		t.Errorf("AllModels() contains duplicate ID %q %d times after concurrent AddRouter", "concurrent", seen["concurrent"])
	}
	if !svc.IsReady() {
		t.Errorf("IsReady() = false after concurrent access, want true (at least one ready router)")
	}
}

// ids is a test helper to extract model IDs for error messages.
func ids(models []domain.Model) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}
