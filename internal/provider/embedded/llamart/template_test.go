package llamart

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hybridgroup/yzma/pkg/llama"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
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
	}, false)
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

func TestGPTOSSTemplateUsesEffortAndThinkingContinuation(t *testing.T) {
	protocol := provider.ResolveModelProtocol("gpt-oss-20b", "")
	messages := []provider.Message{{
		Role:         provider.RoleAssistant,
		ToolCalls:    []provider.ToolCall{{Name: "read_file", Arguments: `{}`}},
		Continuation: &provider.ProviderContinuation{Reasoning: "private plan"},
	}}
	template := `effort={{ reasoning_effort }}{% for message in messages %}{% if message.thinking is defined %}|thinking={{ message.thinking }}{% endif %}{% endfor %}`
	nativeCalls := 0
	rendered, err := renderChatTemplateWithProtocol(template, messages, nil, "high", embedded.ReasoningEffortAuto, false, protocol, func(string, []llama.ChatMessage) (string, error) {
		nativeCalls++
		return "incorrect native rendering", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rendered.text != "effort=high|thinking=private plan" {
		t.Fatalf("rendered = %q", rendered.text)
	}
	if nativeCalls != 0 {
		t.Fatalf("legacy llama_chat_apply_template was used %d times for Harmony", nativeCalls)
	}

	data, err := jinjaTemplateDataForProtocol([]provider.Message{{Role: provider.RoleUser, Content: "hi"}}, nil, "auto", embedded.ReasoningEffortAuto, false, protocol)
	if err != nil {
		t.Fatal(err)
	}
	if data["reasoning_effort"] != "medium" {
		t.Fatalf("default reasoning_effort = %v, want medium", data["reasoning_effort"])
	}
}

func TestTemplateSupportsConfiguredEffortAndPreservedThinking(t *testing.T) {
	messages := []provider.Message{{
		Role:         provider.RoleAssistant,
		Content:      "answer",
		Continuation: &provider.ProviderContinuation{Reasoning: "private reasoning"},
	}}
	template := `{% if reasoning_effort is defined %}effort={{ reasoning_effort }}|{% endif %}{% if preserve_reasoning is defined %}preserve={{ preserve_reasoning }}|{% endif %}{% for message in messages %}{% if message.thinking is defined %}thinking={{ message.thinking }}{% endif %}{% endfor %}`
	rendered, err := renderChatTemplateWithProtocol(
		template,
		messages,
		nil,
		"on",
		embedded.ReasoningEffortMedium,
		true,
		provider.ModelProtocol{},
		func(string, []llama.ChatMessage) (string, error) { return "native", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.text != "effort=medium|preserve=True|thinking=private reasoning" {
		t.Fatalf("rendered = %q", rendered.text)
	}
}

// qwen38EffortTemplateFixture reproduces the reasoning controls of Qwen
// 3.8's official chat_template.jinja: an effort other than xhigh, medium or
// low raises an exception, and thinking is on unless enable_thinking is
// explicitly false.
const qwen38EffortTemplateFixture = `{%- if enable_thinking is undefined or enable_thinking is true %}` +
	`{%- set resolved_reasoning_effort = reasoning_effort|default('xhigh') %}` +
	`{%- if resolved_reasoning_effort not in ('xhigh', 'medium', 'low') %}` +
	`{{- raise_exception('Unexpected reasoning effort ' ~ reasoning_effort) }}` +
	`{%- endif %}effort={{ resolved_reasoning_effort }}` +
	`{%- else %}thinking=off{%- endif %}`

func TestQwen38TemplateReceivesSupportedEffort(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	protocol := provider.ResolveModelProtocol("/models/Qwen3.8-27B-Q4_K_M.gguf", "")
	if protocol.Family != provider.ModelFamilyQwen38 {
		t.Fatalf("protocol = %+v, want Qwen 3.8", protocol)
	}
	native := func(string, []llama.ChatMessage) (string, error) { return "", errors.New("native unsupported") }
	for _, tc := range []struct {
		name      string
		reasoning string
		effort    embedded.ReasoningEffort
		want      string
	}{
		{"think high", "high", embedded.ReasoningEffortAuto, "effort=xhigh"},
		{"think low", "low", embedded.ReasoningEffortAuto, "effort=low"},
		{"think medium", "medium", embedded.ReasoningEffortAuto, "effort=medium"},
		{"configured high", "on", embedded.ReasoningEffortHigh, "effort=xhigh"},
		{"configured low", "on", embedded.ReasoningEffortLow, "effort=low"},
		{"request beats config", "low", embedded.ReasoningEffortHigh, "effort=low"},
		{"on keeps template default", "on", embedded.ReasoningEffortAuto, "effort=xhigh"},
		{"off", "off", embedded.ReasoningEffortAuto, "thinking=off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := renderChatTemplateWithProtocol(qwen38EffortTemplateFixture, messages, nil, tc.reasoning, tc.effort, false, protocol, native)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if rendered.text != tc.want {
				t.Fatalf("rendered = %q, want %q", rendered.text, tc.want)
			}
		})
	}
}

func TestQwen38TemplateRejectsUnmappedHigh(t *testing.T) {
	// Guards the fixture itself: without the high->xhigh mapping (an unknown
	// family), "high" reaches the template and its exception fires.
	messages := []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	native := func(string, []llama.ChatMessage) (string, error) { return "", errors.New("native unsupported") }
	if _, err := renderChatTemplateWithProtocol(qwen38EffortTemplateFixture, messages, nil, "on", embedded.ReasoningEffortHigh, false, provider.ModelProtocol{}, native); err == nil {
		t.Fatal("render succeeded; want the template's unexpected-effort exception")
	}
}

func TestGenericTemplateRejectsEffortReasoningMode(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	if _, err := jinjaTemplateDataForProtocol(messages, nil, "high", embedded.ReasoningEffortAuto, false, provider.ModelProtocol{}); err == nil ||
		!strings.Contains(err.Error(), "invalid reasoning mode") {
		t.Fatalf("err = %v, want invalid reasoning mode", err)
	}
}
