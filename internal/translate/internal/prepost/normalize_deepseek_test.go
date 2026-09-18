package prepost

import (
	"encoding/json"
	"testing"
)

func TestIsDeepSeekModel(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		isDeep  bool
	}{
		{name: "exact", modelID: "deepseek-chat", isDeep: true},
		{name: "flash variant", modelID: "deepseek-v4-flash", isDeep: true},
		{name: "dated variant", modelID: "deepseek-v4-flash-0731", isDeep: true},
		{name: "case insensitive", modelID: "DeepSeek-V3", isDeep: true},
		{name: "other provider", modelID: "qwen-plus", isDeep: false},
		{name: "empty", modelID: "", isDeep: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDeepSeekModel(tt.modelID); got != tt.isDeep {
				t.Errorf("IsDeepSeekModel(%q) = %v, want %v", tt.modelID, got, tt.isDeep)
			}
		})
	}
}

func TestIsDeepSeekFlashModel(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		isFlash bool
	}{
		{name: "v4 flash", modelID: "deepseek-v4-flash", isFlash: true},
		{name: "dated flash", modelID: "deepseek-v4-flash-0731", isFlash: true},
		{name: "colon flash", modelID: "deepseek-v4-flash:0731", isFlash: true},
		{name: "v4.1 flash", modelID: "deepseek-v4.1-flash", isFlash: true},
		{name: "bare flash alias", modelID: "deepseek-flash", isFlash: true},
		{name: "case insensitive", modelID: "DeepSeek-V4-Flash", isFlash: true},
		{name: "chat is not flash", modelID: "deepseek-chat", isFlash: false},
		{name: "r1 is not flash", modelID: "deepseek-r1", isFlash: false},
		{name: "v3 is not flash", modelID: "deepseek-v3", isFlash: false},
		{name: "other provider flash", modelID: "gemini-3-flash", isFlash: false},
		{name: "empty", modelID: "", isFlash: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDeepSeekFlashModel(tt.modelID); got != tt.isFlash {
				t.Errorf("IsDeepSeekFlashModel(%q) = %v, want %v", tt.modelID, got, tt.isFlash)
			}
		})
	}
}

func TestNormalizeDeepSeek(t *testing.T) {
	tests := []struct {
		name      string
		modelID   string
		in        string
		checkRC   any
		checkTopP any
		unchanged bool
	}{
		{
			name:      "injects empty reasoning content",
			modelID:   "deepseek-v4-flash",
			in:        `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			checkRC:   "",
			checkTopP: 0.95,
		},
		{
			name:      "copies reasoning to reasoning content",
			modelID:   "deepseek-chat",
			in:        `{"model":"deepseek-chat","messages":[{"role":"assistant","content":"hi","reasoning":"think"}]}`,
			checkRC:   "think",
			checkTopP: nil,
		},
		{
			name:      "preserves existing reasoning content",
			modelID:   "deepseek-chat",
			in:        `{"model":"deepseek-chat","messages":[{"role":"assistant","content":"hi","reasoning":"a","reasoning_content":"b"}]}`,
			checkRC:   "b",
			checkTopP: nil,
		},
		{
			name:      "preserves explicit top_p",
			modelID:   "deepseek-v4-flash",
			in:        `{"model":"deepseek-v4-flash","top_p":0.5,"messages":[{"role":"user","content":"hi"}]}`,
			checkTopP: 0.5,
		},
		{
			name:      "ignores user messages",
			modelID:   "deepseek-chat",
			in:        `{"model":"deepseek-chat","messages":[{"role":"user","content":"q","reasoning":"noise"}]}`,
			checkTopP: nil,
		},
		{
			name:      "non deepseek untouched",
			modelID:   "qwen-plus",
			in:        `{"model":"qwen-plus","messages":[{"role":"assistant","content":"hi"}]}`,
			unchanged: true,
		},
		{
			name:      "empty model untouched",
			modelID:   "",
			in:        `{"model":"x","messages":[{"role":"assistant","content":"hi"}]}`,
			unchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := NormalizeDeepSeek([]byte(tt.in), tt.modelID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.unchanged {
				if string(out) != tt.in {
					t.Errorf("expected body unchanged, got %s", out)
				}
				return
			}
			var raw map[string]any
			if err := json.Unmarshal(out, &raw); err != nil {
				t.Fatalf("invalid JSON result: %v (body=%s)", err, out)
			}
			if tt.checkRC != nil {
				msgs, _ := raw["messages"].([]any)
				if len(msgs) == 0 {
					t.Fatalf("expected messages, got %s", out)
				}
				last, _ := msgs[len(msgs)-1].(map[string]any)
				if last["role"] == "assistant" && last["reasoning_content"] != tt.checkRC {
					t.Errorf("expected reasoning_content=%v, got %v", tt.checkRC, last["reasoning_content"])
				}
			}
			if tt.checkTopP != nil {
				if raw["top_p"] != tt.checkTopP {
					t.Errorf("expected top_p=%v, got %v", tt.checkTopP, raw["top_p"])
				}
			}
		})
	}
}

func TestNormalizeDeepSeek_UserMessageGetsNoRC(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"q","reasoning":"noise"}]}`)
	out, err := NormalizeDeepSeek(body, "deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	msg := raw["messages"].([]any)[0].(map[string]any)
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("expected reasoning_content to NOT be set for user messages")
	}
}

func TestNormalizeDeepSeek_FlashSetsTopPWithoutMessages(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`)
	out, err := NormalizeDeepSeek(body, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if raw["top_p"] != 0.95 {
		t.Errorf("expected top_p=0.95, got %v", raw["top_p"])
	}
}

func TestNormalizeDeepSeek_InvalidJSON(t *testing.T) {
	if _, err := NormalizeDeepSeek([]byte(`{"messages": invalid}`), "deepseek-chat"); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestNormalizeDeepSeek_Empty(t *testing.T) {
	out, err := NormalizeDeepSeek(nil, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, got %s", out)
	}
}
