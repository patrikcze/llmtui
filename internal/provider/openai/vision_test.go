package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestToWireMessagesTextOnly(t *testing.T) {
	msgs := toWireMessages([]provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
	})
	if len(msgs) != 1 {
		t.Fatalf("len = %d, want 1", len(msgs))
	}
	if content, ok := msgs[0].Content.(string); !ok || content != "hello" {
		t.Errorf("Content = %#v, want plain string", msgs[0].Content)
	}
}

func TestToWireMessagesWithImage(t *testing.T) {
	img := provider.Image{Data: []byte("fake-png-bytes"), MIME: "image/png"}
	msgs := toWireMessages([]provider.Message{
		{Role: provider.RoleUser, Content: "what is this?", Images: []provider.Image{img}},
	})

	parts, ok := msgs[0].Content.([]contentPart)
	if !ok {
		t.Fatalf("Content = %#v, want content parts", msgs[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want text + image", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "what is this?" {
		t.Errorf("part[0] = %+v, want text part", parts[0])
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.Data)
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != wantURL {
		t.Errorf("part[1] = %+v, want image_url with data URL", parts[1])
	}
}

func TestToWireMessagesImageWithoutText(t *testing.T) {
	msgs := toWireMessages([]provider.Message{
		{Role: provider.RoleUser, Images: []provider.Image{{Data: []byte("x")}}},
	})
	parts, ok := msgs[0].Content.([]contentPart)
	if !ok || len(parts) != 1 || parts[0].Type != "image_url" {
		t.Fatalf("Content = %#v, want single image part with png default MIME", msgs[0].Content)
	}
	if !strings.HasPrefix(parts[0].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("URL = %q, want image/png default MIME", parts[0].ImageURL.URL)
	}
}

func TestChatSendsImagePayload(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a cat\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := New("test", srv.URL, "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model: "llava",
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: "describe",
			Images:  []provider.Image{{Data: []byte("img"), MIME: "image/jpeg"}},
		}},
		Stream: true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}

	messages := gotBody["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content parts = %d, want 2", len(content))
	}
	imgPart := content[1].(map[string]any)
	url := imgPart["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Errorf("image url = %q, want jpeg data URL", url)
	}
}

func TestReasoningOnlyStreamFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking hard about MARCO\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" and POLO\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := New("test", srv.URL, "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var text strings.Builder
	for ev := range events {
		if ev.Type == provider.EventDelta {
			text.WriteString(ev.Delta)
		}
	}
	got := text.String()
	if !strings.Contains(got, "thinking hard about MARCO and POLO") {
		t.Errorf("reasoning not surfaced: %q", got)
	}
	if !strings.Contains(got, "raise max_tokens") {
		t.Errorf("fallback should explain the token budget: %q", got)
	}
}

func TestReasoningIgnoredWhenContentPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking…\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"MARCO\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := New("test", srv.URL, "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var text strings.Builder
	for ev := range events {
		if ev.Type == provider.EventDelta {
			text.WriteString(ev.Delta)
		}
	}
	if text.String() != "MARCO" {
		t.Errorf("reply = %q, want only the visible answer", text.String())
	}
}
