package openai

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

// The consumer reads one event, cancels, and stops reading — exactly what the
// TUI does on Esc. The producer goroutine must still return promptly instead
// of blocking forever on an unguarded send (which leaked the goroutine and
// pinned the HTTP connection).
func TestStreamProducerExitsWhenAbandoned(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"c\"}}]}\n" +
		"data: [DONE]\n"
	body := io.NopCloser(strings.NewReader(sse))

	ctx, cancel := context.WithCancel(context.Background())
	p := New("test", "http://x", "")
	events := make(chan provider.ChatEvent)
	done := make(chan struct{})
	go func() {
		p.streamResponse(ctx, body, provider.ChatRequest{}, events)
		close(done)
	}()

	<-events
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamResponse goroutine still blocked after cancel+abandon")
	}
}

func TestWholeResponseProducerExitsWhenAbandoned(t *testing.T) {
	body := io.NopCloser(strings.NewReader(
		`{"choices":[{"message":{"content":"hello"}}]}`))

	ctx, cancel := context.WithCancel(context.Background())
	p := New("test", "http://x", "")
	events := make(chan provider.ChatEvent)
	done := make(chan struct{})
	go func() {
		p.wholeResponse(ctx, body, provider.ChatRequest{}, events)
		close(done)
	}()

	<-events // consume the delta, abandon before EventDone
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wholeResponse goroutine still blocked after cancel+abandon")
	}
}

// temperature/top_p 0 must reach the wire: omitting them silently falls back
// to the server default instead of deterministic sampling.
func TestTemperatureZeroIsSent(t *testing.T) {
	payload, err := json.Marshal(chatCompletionRequest{Model: "m", Temperature: 0, TopP: 0})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"temperature", "top_p"} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("%s missing from wire request when 0: %s", field, payload)
		}
	}
	if _, ok := decoded["max_tokens"]; ok {
		t.Errorf("max_tokens 0 should stay omitted (means unset): %s", payload)
	}
}
