package llamart

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func weatherToolSpec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "weather",
		Description: "Get weather",
		Parameters: []byte(`{
            "type":"object",
            "properties":{
                "city":{"type":"string"},
                "days":{"type":"integer"},
                "metric":{"type":"boolean"},
                "filters":{"type":"object"},
                "tags":{"type":"array"}
            }
        }`),
	}
}

func TestToolOutputRouterSupportedFormats(t *testing.T) {
	tests := []struct {
		name   string
		format embedded.ToolFormat
		raw    string
	}{
		{name: "standard", format: embedded.ToolFormatStandard, raw: `<tool_call>{"name":"weather","arguments":{"city":"Prague","days":2}}</tool_call>`},
		{name: "qwen", format: embedded.ToolFormatQwen, raw: "<function=weather><parameter=city>Prague</parameter><parameter=days>2</parameter></function>"},
		{name: "glm", format: embedded.ToolFormatGLM, raw: "weather<arg_key>city</arg_key><arg_value>Prague</arg_value><arg_key>days</arg_key><arg_value>2</arg_value>"},
		{name: "mistral", format: embedded.ToolFormatMistral, raw: `[TOOL_CALLS]weather[ARGS]{"city":"Prague","days":2}`},
		{name: "gemma", format: embedded.ToolFormatGemma, raw: `<|toolcall>call:weather{city:<|"|>Prague<|"|>,days:2}<toolcall|>`},
		{name: "gpt", format: embedded.ToolFormatGPT, raw: `.weather <|message|>{"city":"Prague","days":2}`},
		{name: "phi", format: embedded.ToolFormatPhi, raw: `<|tool_call>{"name":"weather","arguments":{"city":"Prague","days":2}}</tool_call>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := newToolOutputRouter(test.format, []provider.ToolSpec{weatherToolSpec()})
			var visible strings.Builder
			for _, piece := range []string{test.raw[:len(test.raw)/2], test.raw[len(test.raw)/2:]} {
				for _, text := range router.Push(piece) {
					visible.WriteString(text)
				}
			}
			tail, calls, err := router.Finish()
			if err != nil {
				t.Fatalf("Finish: %v", err)
			}
			visible.WriteString(strings.Join(tail, ""))
			if visible.String() != "" {
				t.Errorf("tool markup leaked as visible text: %q", visible.String())
			}
			if len(calls) != 1 || calls[0].Name != "weather" {
				t.Fatalf("calls = %+v", calls)
			}
			var arguments map[string]any
			if err := json.Unmarshal([]byte(calls[0].Arguments), &arguments); err != nil {
				t.Fatalf("arguments are not JSON: %v", err)
			}
			if arguments["city"] != "Prague" || arguments["days"] != float64(2) {
				t.Errorf("arguments = %+v", arguments)
			}
		})
	}
}

func TestToolOutputRouterPreservesTypedNestedArguments(t *testing.T) {
	raw := `<tool_call>{"name":"weather","arguments":{"city":"Žluťoučký kůň","days":3,"metric":true,"filters":{"rain":{"max":2.5}},"tags":["city",4]}}</tool_call>`
	router := newToolOutputRouter(embedded.ToolFormatStandard, []provider.ToolSpec{weatherToolSpec()})
	router.Push(raw)
	_, calls, err := router.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(calls[0].Arguments), &arguments); err != nil {
		t.Fatal(err)
	}
	if arguments["city"] != "Žluťoučký kůň" || arguments["days"] != float64(3) || arguments["metric"] != true {
		t.Errorf("scalar argument types = %+v", arguments)
	}
	filters, ok := arguments["filters"].(map[string]any)
	if !ok || filters["rain"].(map[string]any)["max"] != 2.5 {
		t.Errorf("nested object = %+v", arguments["filters"])
	}
	tags, ok := arguments["tags"].([]any)
	if !ok || len(tags) != 2 || tags[1] != float64(4) {
		t.Errorf("array = %+v", arguments["tags"])
	}
}

func TestToolOutputRouterMultipleCallsAndMixedSpeech(t *testing.T) {
	raw := "I will check both.\n" +
		`<tool_call>{"name":"weather","arguments":{"city":"Prague"}}</tool_call>` +
		`<tool_call>{"name":"weather","arguments":{"city":"Brno"}}</tool_call>` +
		"\nWaiting for results."
	router := newToolOutputRouter(embedded.ToolFormatStandard, []provider.ToolSpec{weatherToolSpec()})
	var visible strings.Builder
	for _, piece := range []string{raw[:8], raw[8:23], raw[23:]} {
		visible.WriteString(strings.Join(router.Push(piece), ""))
	}
	tail, calls, err := router.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	visible.WriteString(strings.Join(tail, ""))
	if len(calls) != 2 || !strings.Contains(visible.String(), "I will check both.") || !strings.Contains(visible.String(), "Waiting for results.") {
		t.Errorf("visible=%q calls=%+v", visible.String(), calls)
	}
	if strings.Contains(visible.String(), "tool_call") {
		t.Errorf("markup leaked: %q", visible.String())
	}
}

func TestToolOutputRouterOrdinaryResponseStreamsWithToolsOffered(t *testing.T) {
	router := newToolOutputRouter(embedded.ToolFormatStandard, []provider.ToolSpec{weatherToolSpec()})
	first := router.Push("Ordinary streamed ")
	if strings.Join(first, "") != "Ordinary streamed " {
		t.Fatalf("first Push = %q, want immediate ordinary text", strings.Join(first, ""))
	}
	second := router.Push("answer.")
	tail, calls, err := router.Finish()
	if err != nil || len(calls) != 0 {
		t.Fatalf("Finish calls=%+v err=%v", calls, err)
	}
	if got := strings.Join(first, "") + strings.Join(second, "") + strings.Join(tail, ""); got != "Ordinary streamed answer." {
		t.Errorf("visible = %q", got)
	}
}

func TestToolOutputRouterRejectsMalformedAndUnknownCalls(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		router := newToolOutputRouter(embedded.ToolFormatStandard, []provider.ToolSpec{weatherToolSpec()})
		router.Push(`<tool_call>{"name":"weather","arguments":{"city":"Prague"}`)
		if _, _, err := router.Finish(); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("Finish error = %v", err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		router := newToolOutputRouter(embedded.ToolFormatStandard, []provider.ToolSpec{weatherToolSpec()})
		router.Push(`<tool_call>{"name":"delete_everything","arguments":{}}</tool_call>`)
		if _, _, err := router.Finish(); err == nil || !strings.Contains(err.Error(), "unknown tool") || !strings.Contains(err.Error(), "weather") {
			t.Fatalf("Finish error = %v", err)
		}
	})
	t.Run("invalid typed argument", func(t *testing.T) {
		router := newToolOutputRouter(embedded.ToolFormatStandard, []provider.ToolSpec{weatherToolSpec()})
		router.Push(`<tool_call>{"name":"weather","arguments":{"days":"many"}}</tool_call>`)
		if _, _, err := router.Finish(); err == nil || !strings.Contains(err.Error(), "expected a JSON number") {
			t.Fatalf("Finish error = %v", err)
		}
	})
}

func TestToolOutputRouterGemmaZeroArgumentCallsHonorSchema(t *testing.T) {
	pathless := provider.ToolSpec{
		Name:        "list_dir",
		Description: "List the project root when path is omitted",
		Parameters:  []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}

	t.Run("optional arguments", func(t *testing.T) {
		router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{pathless})
		router.Push(`<|tool_call>call:list_dir{}<tool_call|>`)
		visible, calls, err := router.Finish()
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if len(visible) != 0 || len(calls) != 1 || calls[0].Name != "list_dir" || calls[0].Arguments != `{}` {
			t.Fatalf("visible=%q calls=%+v", strings.Join(visible, ""), calls)
		}
	})

	t.Run("required argument omitted", func(t *testing.T) {
		required := weatherToolSpec()
		required.Parameters = []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
		router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{required})
		router.Push(`<|tool_call>call:weather{}<tool_call|>`)
		if _, _, err := router.Finish(); err == nil || !strings.Contains(err.Error(), `missing required argument "city"`) {
			t.Fatalf("Finish error = %v", err)
		}
	})
}

func TestToolOutputRouterStripsSimulatedResults(t *testing.T) {
	raw := `<|toolcall>call:weather{city:<|"|>Prague<|"|>}<toolcall|><toolresult>{"status":"sunny"}</toolresult>spoken`
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{weatherToolSpec()})
	router.Push(raw)
	tail, calls, err := router.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(calls) != 1 || strings.Join(tail, "") != "spoken" {
		t.Errorf("tail=%q calls=%+v", strings.Join(tail, ""), calls)
	}
}

func TestToolOutputRouterEveryMarkerSplitPoint(t *testing.T) {
	for _, test := range []struct {
		name   string
		format embedded.ToolFormat
		raw    string
	}{
		{name: "standard", format: embedded.ToolFormatStandard, raw: `speech<tool_call>{"name":"weather","arguments":{"city":"Prague"}}</tool_call>`},
		{name: "gemma", format: embedded.ToolFormatGemma, raw: `speech<|toolcall>call:weather{city:<|"|>Prague<|"|>}<toolcall|>`},
		{name: "gemma underscore", format: embedded.ToolFormatGemma, raw: `speech<|tool_call>call:weather{city:<|"|>Prague<|"|>}<tool_call|>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for split := 0; split <= len(test.raw); split++ {
				router := newToolOutputRouter(test.format, []provider.ToolSpec{weatherToolSpec()})
				visible := strings.Join(router.Push(test.raw[:split]), "") + strings.Join(router.Push(test.raw[split:]), "")
				tail, calls, err := router.Finish()
				visible += strings.Join(tail, "")
				if err != nil || len(calls) != 1 || strings.Contains(visible, "tool") || strings.Contains(visible, "call:") || visible != "speech" {
					t.Fatalf("split %d: visible=%q calls=%+v err=%v", split, visible, calls, err)
				}
			}
		})
	}
}

func TestPrepareToolMessagesIsDeterministicAndPreservesHistory(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "Be concise."},
		{Role: provider.RoleAssistant, Content: "Checking", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "weather", Arguments: `{"city":"Prague"}`}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "weather", Content: `{"temp":21}`},
	}
	first, err := prepareToolMessages(messages, []provider.ToolSpec{weatherToolSpec()}, embedded.ToolFormatGemma)
	if err != nil {
		t.Fatalf("prepareToolMessages: %v", err)
	}
	second, err := prepareToolMessages(messages, []provider.ToolSpec{weatherToolSpec()}, embedded.ToolFormatGemma)
	if err != nil {
		t.Fatalf("prepareToolMessages: %v", err)
	}
	if first[0].Content != second[0].Content || !strings.Contains(first[0].Content, `"name":"weather"`) || !strings.Contains(first[0].Content, "call:NAME") {
		t.Errorf("instruction is not deterministic/complete: %q", first[0].Content)
	}
	if len(first[1].ToolCalls) != 1 || first[2].ToolCallID != "c1" || messages[0].Content != "Be concise." {
		t.Errorf("history mutated or lost: prepared=%+v original=%+v", first, messages)
	}
}

func TestPrepareToolMessagesAddsGemmaFollowupToClonedUserTurn(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "list dir"}}
	prepared, err := prepareToolMessages(messages, []provider.ToolSpec{weatherToolSpec()}, embedded.ToolFormatGemma)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 2 || !strings.Contains(prepared[1].Content, gemmaToolFollowupInstruction) {
		t.Fatalf("prepared messages = %+v", prepared)
	}
	if messages[0].Content != "list dir" {
		t.Fatalf("source message mutated: %q", messages[0].Content)
	}

	standard, err := prepareToolMessages(messages, []provider.ToolSpec{weatherToolSpec()}, embedded.ToolFormatStandard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(standard[len(standard)-1].Content, gemmaToolFollowupInstruction) {
		t.Fatalf("non-Gemma user turn changed: %+v", standard)
	}
}
