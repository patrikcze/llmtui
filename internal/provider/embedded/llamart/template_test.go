package llamart

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hybridgroup/yzma/pkg/llama"

	"github.com/patrikcze/llmtui/internal/provider"
)

// gemma4TemplateFixture is deliberately small, but uses the constructs that
// make the real Gemma 4 metadata template fall outside llama.cpp's restricted
// template renderer: macros, namespace mutation, dictsort, tools, multimodal
// content, and an optional enable_thinking variable.
const gemma4TemplateFixture = `{%- macro render_content(content) -%}
{%- if content is string -%}{{ content }}
{%- else -%}{%- for item in content -%}[{{ item.type }}]{% if item.text is defined %}{{ item.text }}{% endif %}{%- endfor -%}
{%- endif -%}{%- endmacro -%}
{%- set ns = namespace(found=false) -%}
{%- if tools is defined -%}
{%- for tool in tools -%}
TOOL={{ tool.function.name }}(
{%- for key, value in tool.function.parameters.properties | dictsort -%}
{%- if ns.found %},{% endif -%}{%- set ns.found = true -%}{{ key }}:{{ value.type }}
{%- endfor -%})
{%- endfor -%}
{%- endif -%}
{%- for message in messages -%}<|turn>{{ message.role }}
{{ render_content(message.content) }}<turn|>
{%- endfor -%}
{%- if enable_thinking is defined and enable_thinking -%}<|think|>{%- endif -%}
{%- if add_generation_prompt -%}<|turn>model
{%- endif -%}`

func TestRenderChatTemplateFallsBackFromUnsupportedNativeRenderer(t *testing.T) {
	nativeErr := errors.New("template is invalid or unsupported")
	rendered, err := renderChatTemplate(
		gemma4TemplateFixture,
		[]provider.Message{{Role: provider.RoleUser, Content: "Hi"}},
		nil,
		"auto",
		func(string, []llama.ChatMessage) (string, error) { return "", nativeErr },
	)
	if err != nil {
		t.Fatalf("renderChatTemplate fallback: %v", err)
	}
	if !strings.Contains(rendered.text, "<|turn>user\nHi<turn|>") || !strings.HasSuffix(rendered.text, "<|turn>model") {
		t.Fatalf("fallback rendered prompt = %q", rendered.text)
	}
	if strings.Contains(rendered.text, "<|think|>") {
		t.Fatalf("auto fallback unexpectedly enabled thinking: %q", rendered.text)
	}
}

func TestRenderChatTemplateReasoningToolsAndImages(t *testing.T) {
	messages := []provider.Message{{
		Role:    provider.RoleUser,
		Content: "describe",
		Images:  []provider.Image{{Data: []byte("image"), MIME: "image/png"}},
	}}
	tools := []provider.ToolSpec{{
		Name:        "weather",
		Description: "Get weather",
		Parameters:  []byte(`{"type":"object","properties":{"units":{"type":"string"},"city":{"type":"string"}}}`),
	}}
	nativeCalls := 0
	native := func(string, []llama.ChatMessage) (string, error) {
		nativeCalls++
		return "native", nil
	}

	for _, test := range []struct {
		mode      string
		wantThink bool
	}{
		{mode: "auto"},
		{mode: "on", wantThink: true},
		{mode: "off"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			rendered, err := renderChatTemplate(gemma4TemplateFixture, messages, tools, test.mode, native)
			if err != nil {
				t.Fatalf("renderChatTemplate: %v", err)
			}
			if strings.Contains(rendered.text, "<|think|>") != test.wantThink {
				t.Errorf("rendered thinking marker = %t, want %t: %q", strings.Contains(rendered.text, "<|think|>"), test.wantThink, rendered.text)
			}
			for _, want := range []string{"TOOL=weather", "city:string", "units:string", "[image]", "[text]describe"} {
				if !strings.Contains(rendered.text, want) {
					t.Errorf("rendered prompt %q missing %q", rendered.text, want)
				}
			}
		})
	}
	if nativeCalls != 0 {
		t.Errorf("native renderer called %d times; requests with tools must go directly to Jinja", nativeCalls)
	}
}

func TestRenderChatTemplatePreservesBothFailures(t *testing.T) {
	nativeErr := errors.New("native rejected fixture")
	_, err := renderChatTemplate(
		"{% broken",
		[]provider.Message{{Role: provider.RoleUser, Content: "Hi"}},
		nil,
		"auto",
		func(string, []llama.ChatMessage) (string, error) { return "", nativeErr },
	)
	if err == nil {
		t.Fatal("renderChatTemplate error = nil")
	}
	message := err.Error()
	for _, want := range []string{"native rejected fixture", "jinja fallback", "providers.<name>.chat_template"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q missing %q", message, want)
		}
	}
}

func TestRenderChatTemplateKeepsNativeCompatibilityPath(t *testing.T) {
	want := "native prompt"
	rendered, err := renderChatTemplate(
		"ignored",
		[]provider.Message{{Role: provider.RoleUser, Content: "Hi"}},
		nil,
		"",
		func(string, []llama.ChatMessage) (string, error) { return want, nil },
	)
	if err != nil {
		t.Fatalf("renderChatTemplate: %v", err)
	}
	if rendered.text != want {
		t.Errorf("rendered = %q, want native result %q", rendered.text, want)
	}
}

func TestTemplateMessagesPreserveToolContinuation(t *testing.T) {
	mapped := templateMessages([]provider.Message{
		{
			Role:    provider.RoleAssistant,
			Content: "I will check.",
			ToolCalls: []provider.ToolCall{{
				ID:        "call-1",
				Name:      "weather",
				Arguments: `{"city":"Prague","days":2}`,
			}},
		},
		{Role: provider.RoleTool, ToolCallID: "call-1", ToolName: "weather", Content: `{"temp":21}`},
	})
	assistant := mapped[0].(map[string]any)
	if assistant["content"] != "I will check." {
		t.Errorf("assistant content = %v", assistant["content"])
	}
	calls := assistant["tool_calls"].([]any)
	function := calls[0].(map[string]any)["function"].(map[string]any)
	arguments := function["arguments"].(map[string]any)
	if function["name"] != "weather" || arguments["city"] != "Prague" || arguments["days"].(json.Number).String() != "2" {
		t.Errorf("tool continuation = %+v", function)
	}
	toolResult := mapped[1].(map[string]any)
	if toolResult["tool_call_id"] != "call-1" || toolResult["name"] != "weather" {
		t.Errorf("tool result = %+v", toolResult)
	}
}
