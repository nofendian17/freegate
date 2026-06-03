package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

// TokenUsage holds token counts extracted from an upstream response.
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
}

func writeNonStreaming(w http.ResponseWriter, resp *anyllm.ChatCompletion, usage *TokenUsage) {
	if usage != nil && resp.Usage != nil {
		usage.Prompt = resp.Usage.PromptTokens
		usage.Completion = resp.Usage.CompletionTokens
		usage.Total = resp.Usage.TotalTokens
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	body, err := json.Marshal(resp)
	if err != nil {
		_, _ = w.Write([]byte(`{"error":{"type":"internal_error","message":"failed to serialize response"}}`))
		return
	}
	_, _ = w.Write(body)
}

func writeStreaming(w http.ResponseWriter, chunks <-chan anyllm.ChatCompletionChunk, errs <-chan error, usage *TokenUsage) error {
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// Note: we do NOT call WriteHeader(200) here. If the upstream returns
	// an error before sending any chunk, the caller needs the freedom to
	// switch to a 502 JSON response. Once we emit a chunk, we commit to
	// 200 + SSE; the caller cannot change the status after that.
	emitted := 0
	for chunk := range chunks {
		if emitted == 0 {
			w.WriteHeader(http.StatusOK)
			if fl != nil {
				fl.Flush()
			}
		}
		if usage != nil && chunk.Usage != nil {
			usage.Prompt = chunk.Usage.PromptTokens
			usage.Completion = chunk.Usage.CompletionTokens
			usage.Total = chunk.Usage.TotalTokens
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("serialize chunk: %w", err)
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		emitted++
	}
	for err := range errs {
		if err != nil && emitted == 0 {
			return err
		}
	}
	if emitted == 0 {
		// Channel closed without any chunks and no error — treat as
		// upstream finished cleanly with empty stream.
		return nil
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if fl != nil {
		fl.Flush()
	}
	return nil
}
