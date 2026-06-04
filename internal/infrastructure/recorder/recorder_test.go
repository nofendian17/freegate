package recorder

import (
	"testing"

	"freegate/internal/model"
)

func TestRecorderRecordAndSnapshot(t *testing.T) {
	r := NewRecorder(func() map[string]any { return nil })
	r.SetModelsFunc(func() []model.Model { return nil })

	for i := 0; i < 5; i++ {
		r.RecordRequestLog(model.RequestLogEntry{
			Method: "POST", Path: "/v1/chat/completions", Model: "m", Upstream: "opencode",
			Status: 200, DurationMs: 100, IP: "1.2.3.4",
		})
	}
	got := r.Requests()
	if len(got) != 5 {
		t.Errorf("len(Requests) = %d, want 5", len(got))
	}
}

func TestRecorderOverflow(t *testing.T) {
	r := NewRecorder(func() map[string]any { return nil })
	for i := 0; i < 150; i++ {
		r.RecordRequestLog(model.RequestLogEntry{Model: "m"})
	}
	if got := r.Requests(); len(got) != 100 {
		t.Errorf("after 150 pushes len = %d, want 100 (cap)", len(got))
	}
}
