package ollama

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestToWireMessagesEncodesImages(t *testing.T) {
	data := []byte("fake-image")
	msgs := toWireMessages([]provider.Message{
		{Role: provider.RoleUser, Content: "what is this?", Images: []provider.Image{{Data: data, MIME: "image/png"}}},
		{Role: provider.RoleAssistant, Content: "a cat"},
	})

	if len(msgs) != 2 {
		t.Fatalf("len = %d, want 2", len(msgs))
	}
	if len(msgs[0].Images) != 1 || msgs[0].Images[0] != base64.StdEncoding.EncodeToString(data) {
		t.Errorf("Images = %v, want base64 of data", msgs[0].Images)
	}
	if msgs[1].Images != nil {
		t.Errorf("text-only message should have no images field, got %v", msgs[1].Images)
	}
}

func TestChatSendsImagePayload(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		fmt.Fprintln(w, `{"message":{"content":"a dog"},"done":true,"prompt_eval_count":1,"eval_count":1}`)
	}))
	defer srv.Close()

	p := New(srv.URL)
	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model: "llava",
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: "describe",
			Images:  []provider.Image{{Data: []byte("img-bytes")}},
		}},
		Stream: true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}

	if len(got.Messages) != 1 || len(got.Messages[0].Images) != 1 {
		t.Fatalf("messages = %+v, want one message with one image", got.Messages)
	}
	if got.Messages[0].Images[0] != base64.StdEncoding.EncodeToString([]byte("img-bytes")) {
		t.Errorf("image = %q, want base64 payload", got.Messages[0].Images[0])
	}
}
