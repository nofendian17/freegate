package metrics

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestMetrics_New(t *testing.T) {
	m := New()
	if m == nil {
		t.Fatal("expected non-nil Metrics")
	}

	snap := m.Snapshot()
	if snap["total_requests"].(int64) != 0 {
		t.Errorf("expected total_requests=0, got %v", snap["total_requests"])
	}
	if snap["retry_count"].(int64) != 0 {
		t.Errorf("expected retry_count=0, got %v", snap["retry_count"])
	}
	if snap["rate_limit_hits"].(int64) != 0 {
		t.Errorf("expected rate_limit_hits=0, got %v", snap["rate_limit_hits"])
	}
	if snap["upstream_errors"].(int64) != 0 {
		t.Errorf("expected upstream_errors=0, got %v", snap["upstream_errors"])
	}
	if snap["total_tokens"].(int64) != 0 {
		t.Errorf("expected total_tokens=0, got %v", snap["total_tokens"])
	}
}

func TestMetrics_IncrTotalRequests(t *testing.T) {
	m := New()
	m.TotalRequests.Add(1)
	m.TotalRequests.Add(1)
	m.TotalRequests.Add(1)

	snap := m.Snapshot()
	if snap["total_requests"].(int64) != 3 {
		t.Errorf("expected total_requests=3, got %v", snap["total_requests"])
	}
}

func TestMetrics_IncrRetryCount(t *testing.T) {
	m := New()
	m.RetryCount.Add(2)

	snap := m.Snapshot()
	if snap["retry_count"].(int64) != 2 {
		t.Errorf("expected retry_count=2, got %v", snap["retry_count"])
	}
}

func TestMetrics_IncrRateLimitHits(t *testing.T) {
	m := New()
	m.RateLimitHits.Add(5)

	snap := m.Snapshot()
	if snap["rate_limit_hits"].(int64) != 5 {
		t.Errorf("expected rate_limit_hits=5, got %v", snap["rate_limit_hits"])
	}
}

func TestMetrics_IncrUpstreamErrors(t *testing.T) {
	m := New()
	m.UpstreamErrors.Add(1)

	snap := m.Snapshot()
	if snap["upstream_errors"].(int64) != 1 {
		t.Errorf("expected upstream_errors=1, got %v", snap["upstream_errors"])
	}
}

func TestMetrics_IncrUpstream(t *testing.T) {
	m := New()
	m.IncrUpstream("opencode")
	m.IncrUpstream("opencode")
	m.IncrUpstream("kilo")

	snap := m.Snapshot()
	perUp := snap["per_upstream"].(map[string]int64)
	if perUp["opencode"] != 2 {
		t.Errorf("expected opencode=2, got %d", perUp["opencode"])
	}
	if perUp["kilo"] != 1 {
		t.Errorf("expected kilo=1, got %d", perUp["kilo"])
	}
}

func TestMetrics_IncrUpstream_Concurrent(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.IncrUpstream("opencode")
		}()
	}
	wg.Wait()

	snap := m.Snapshot()
	perUp := snap["per_upstream"].(map[string]int64)
	if perUp["opencode"] != 100 {
		t.Errorf("expected opencode=100, got %d", perUp["opencode"])
	}
}

func TestMetrics_Snapshot_JSON(t *testing.T) {
	m := New()
	m.TotalRequests.Add(10)
	m.IncrUpstream("kilo")

	snap := m.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("failed to marshal snapshot: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}

	if result["total_requests"].(float64) != 10 {
		t.Errorf("expected total_requests=10 in JSON, got %v", result["total_requests"])
	}
}

func TestMetrics_Snapshot_ReturnsCopy(t *testing.T) {
	m := New()
	m.TotalRequests.Add(1)

	snap1 := m.Snapshot()
	snap2 := m.Snapshot()

	// Modify snap1 shouldn't affect snap2
	snap1["total_requests"] = int64(999)
	if snap2["total_requests"].(int64) != 1 {
		t.Errorf("expected snapshot independence, got %v", snap2["total_requests"])
	}
}
