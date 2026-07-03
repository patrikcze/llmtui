package ollama

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

// Same abandonment scenario as the openai provider: the consumer reads one
// event, cancels, and walks away; the producer must still return.
func TestStreamProducerExitsWhenAbandoned(t *testing.T) {
	ndjson := `{"message":{"content":"a"},"done":false}` + "\n" +
		`{"message":{"content":"b"},"done":false}` + "\n" +
		`{"message":{"content":"c"},"done":true}` + "\n"
	body := io.NopCloser(strings.NewReader(ndjson))

	ctx, cancel := context.WithCancel(context.Background())
	p := New("http://x")
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

func TestTemperatureZeroIsSent(t *testing.T) {
	payload, err := json.Marshal(chatOptions{Temperature: 0, TopP: 0})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"temperature", "top_p"} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("%s missing from wire options when 0: %s", field, payload)
		}
	}
	if _, ok := decoded["num_predict"]; ok {
		t.Errorf("num_predict 0 should stay omitted (means unset): %s", payload)
	}
}
