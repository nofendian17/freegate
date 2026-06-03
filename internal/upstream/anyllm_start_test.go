package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAnyLLMProvider_Start_PopulatesCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m1","object":"model","created":1,"owned_by":"test"}]}`))
	}))
	defer srv.Close()

	p, err := NewAnyLLMProvider("test", srv.URL, "k", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("NewAnyLLMProvider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, 50*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.Models()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := p.Models()
	if len(got) != 1 || got[0].ID != "m1" {
		t.Errorf("Models() = %+v, want one model m1", got)
	}
}
