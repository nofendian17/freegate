package translate

import (
	"encoding/json"
	"testing"
)

// TestRequest_ClaudeThinkingSurvivesToOpenAIBody verifies that a Claude
// request's `thinking` config (extended thinking / budget_tokens) is not
// silently dropped when translating Claude -> OpenAI. Previously
// claude.ToOpenAI never copied the field into the OpenAI-intermediate
// body, so prepost.NormalizeThinkingConfig and prepost.AdjustMaxTokens
// (which both read body.thinking) never saw it, and the reasoning
// configuration was lost before ever reaching the upstream.
func TestRequest_ClaudeThinkingSurvivesToOpenAIBody(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4",
		"max_tokens": 100,
		"thinking": {"type": "enabled", "budget_tokens": 4096},
		"messages": [{"role": "user", "content": "hi"}]
	}`

	out, err := Request([]byte(body), FormatClaude, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON result: %v (body=%s)", err, out)
	}

	th, ok := got["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking to survive translation, got body=%s", out)
	}
	if th["budget_tokens"] != float64(4096) {
		t.Errorf("expected budget_tokens=4096, got %v", th["budget_tokens"])
	}

	// AdjustMaxTokens must have seen the budget and bumped max_tokens
	// strictly above it (max_tokens=100 <= budget=4096).
	if mt, _ := got["max_tokens"].(float64); mt <= 4096 {
		t.Errorf("expected max_tokens bumped above thinking budget, got %v", got["max_tokens"])
	}
}

// TestRequest_ClaudeMetadataUserIDMapsToOpenAIUser verifies Claude's
// nested metadata.user_id maps to OpenAI's top-level `user` field, rather
// than being copied wholesale into a nonstandard `user_id` key.
func TestRequest_ClaudeMetadataUserIDMapsToOpenAIUser(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4",
		"max_tokens": 100,
		"metadata": {"user_id": "user-123"},
		"messages": [{"role": "user", "content": "hi"}]
	}`

	out, err := Request([]byte(body), FormatClaude, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if got["user"] != "user-123" {
		t.Errorf("expected user=user-123, got %v (body=%s)", got["user"], out)
	}
	if _, present := got["user_id"]; present {
		t.Errorf("did not expect a user_id key in the OpenAI body, got %s", out)
	}
}

// TestRequest_ClaudeTopKSurvives verifies top_k isn't silently dropped.
func TestRequest_ClaudeTopKSurvives(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4",
		"max_tokens": 100,
		"top_k": 40,
		"messages": [{"role": "user", "content": "hi"}]
	}`

	out, err := Request([]byte(body), FormatClaude, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if got["top_k"] != float64(40) {
		t.Errorf("expected top_k=40, got %v (body=%s)", got["top_k"], out)
	}
}

// TestRequest_ClaudeImageURLSource verifies that an image content block
// using the {"type":"url",...} source shape is translated to a usable
// image_url, instead of the previous behavior of always assuming base64
// and emitting a broken "data:image/png;base64," URL with no data.
func TestRequest_ClaudeImageURLSource(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": [
			{"type": "image", "source": {"type": "url", "url": "https://example.com/cat.png"}}
		]}]
	}`

	out, err := Request([]byte(body), FormatClaude, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("expected at least one message, got body=%s", out)
	}
	msg, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := msg["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("expected content parts, got msg=%v", msg)
	}
	part, _ := content[0].(map[string]any)
	if part["type"] != "image_url" {
		t.Fatalf("expected type=image_url, got %v", part["type"])
	}
	imageURL, _ := part["image_url"].(map[string]any)
	if imageURL["url"] != "https://example.com/cat.png" {
		t.Errorf("expected the original URL to be forwarded, got %v", imageURL["url"])
	}
}
