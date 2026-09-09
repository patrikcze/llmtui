package llamart

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func TestToolOutputRouterReturnsTypedUnofferedToolError(t *testing.T) {
	router := newToolOutputRouter(
		embedded.ToolFormatStandard,
		[]provider.ToolSpec{weatherToolSpec()},
	)
	router.Push(`<tool_call>{"name":"mcp__playwright__browser_type","arguments":{"target":"ref=e42"}}</tool_call>`)

	_, _, err := router.Finish()
	var unoffered *provider.ToolNotOfferedError
	if !errors.As(err, &unoffered) {
		t.Fatalf("Finish error = %T %v, want *provider.ToolNotOfferedError", err, err)
	}
	if unoffered.RequestedName != "mcp__playwright__browser_type" {
		t.Fatalf("requested = %q", unoffered.RequestedName)
	}
	if len(unoffered.OfferedNames) != 1 || unoffered.OfferedNames[0] != "weather" {
		t.Fatalf("offered = %v", unoffered.OfferedNames)
	}
}

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

func TestToolOutputRouterGemmaAlternateDelimitersDoNotLeak(t *testing.T) {
	tool := provider.ToolSpec{
		Name:       "web_search",
		Parameters: []byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	}
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
	var visible strings.Builder
	for _, piece := range []string{`<|tool`, `call|call:web_`, `search{query:<|"|>current stable Go programming language `, `version release date<|"|>}<|toolcall|>`} {
		visible.WriteString(strings.Join(router.Push(piece), ""))
	}
	tail, calls, err := router.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	visible.WriteString(strings.Join(tail, ""))
	if visible.String() != "" {
		t.Fatalf("tool markup leaked as visible text: %q", visible.String())
	}
	if len(calls) != 1 || calls[0].Name != "web_search" {
		t.Fatalf("calls = %+v", calls)
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(calls[0].Arguments), &arguments); err != nil {
		t.Fatalf("arguments are not JSON: %v", err)
	}
	if arguments["query"] != "current stable Go programming language version release date" {
		t.Fatalf("arguments = %+v", arguments)
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

// TestToolOutputRouterGemmaRepairsScalarForArrayArgument reproduces a live
// failure: Gemma 4 E4B, calling a personal_apps operation whose schema
// declares a string-array argument (e.g. mail_search's account_ids),
// consistently omitted the [] brackets around a single id. The Gemma text
// format has no native array syntax of its own — a bare/quoted scalar is
// literally what the model has available for "here is one value" — so this
// is treated as a repair, not a rejection.
func TestToolOutputRouterGemmaRepairsScalarForArrayArgument(t *testing.T) {
	tool := provider.ToolSpec{
		Name: "mail_search",
		Parameters: []byte(`{
			"type":"object",
			"properties":{"account_ids":{"type":"array","items":{"type":"string"}}}
		}`),
	}
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
	router.Push(`<|toolcall>call:mail_search{account_ids:<|"|>acct_1<|"|>}<toolcall|>`)
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
	ids, ok := arguments["account_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "acct_1" {
		t.Fatalf("account_ids = %+v, want [\"acct_1\"]", arguments["account_ids"])
	}
}

// TestToolOutputRouterGemmaRepairsBracketedArrayArgument reproduces the
// live failure that followed the fix above: once told (via a prior tool
// error) to wrap the value in [], Gemma 4 E4B did so — but
// github.com/hybridgroup/yzma's Gemma parser has no case for "[" at all,
// so the whole bracketed expression, quote tokens included, arrived as one
// unparsed string: "[<|\"|>box_1<|\"|>]". Confirmed by reading
// parser_gemma.go directly (not guessed): parseGemmaArgs recognizes
// Gemma-quote-wrapped, JSON-double-quoted and nested {...} values, and
// falls back to reading straight through to the next top-level comma/brace
// for anything else — including a leading "[".
func TestToolOutputRouterGemmaRepairsBracketedArrayArgument(t *testing.T) {
	tool := provider.ToolSpec{
		Name: "mail_search",
		Parameters: []byte(`{
			"type":"object",
			"properties":{"mailbox_ids":{"type":"array","items":{"type":"string"}}}
		}`),
	}
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
	router.Push(`<|toolcall>call:mail_search{mailbox_ids:[<|"|>box_9ac61a14534255d6d6822b57<|"|>]}<toolcall|>`)
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
	ids, ok := arguments["mailbox_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "box_9ac61a14534255d6d6822b57" {
		t.Fatalf("mailbox_ids = %+v, want [\"box_9ac61a14534255d6d6822b57\"]", arguments["mailbox_ids"])
	}
}

func TestSplitGemmaBracketedArray(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
		ok   bool
	}{
		{
			name: "single Gemma-quoted element",
			raw:  `[<|"|>box_1<|"|>]`,
			want: []string{"box_1"}, ok: true,
		},
		{
			name: "multiple Gemma-quoted elements",
			raw:  `[<|"|>box_1<|"|>,<|"|>box_2<|"|>]`,
			want: []string{"box_1", "box_2"}, ok: true,
		},
		{
			name: "bare unquoted element",
			raw:  `[box_1]`,
			want: []string{"box_1"}, ok: true,
		},
		{
			name: "standard JSON-quoted element",
			raw:  `["box_1"]`,
			want: []string{"box_1"}, ok: true,
		},
		{
			name: "a comma embedded inside a quote token is not a split point",
			raw:  `[<|"|>a, b<|"|>]`,
			want: []string{"a, b"}, ok: true,
		},
		{name: "empty array has nothing to repair", raw: `[]`, ok: false},
		{name: "not bracketed at all", raw: `box_1`, ok: false},
		{name: "unterminated quote token", raw: `[<|"|>box_1]`, ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := splitGemmaBracketedArray(tc.raw)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %v)", ok, tc.ok, got)
			}
			if !ok {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestNormalizeArgumentArrayRepair(t *testing.T) {
	stringItems := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	intItems := map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}
	untypedArray := map[string]any{"type": "array"}
	objectItems := map[string]any{"type": "array", "items": map[string]any{"type": "object"}}

	tests := []struct {
		name    string
		raw     string
		schema  map[string]any
		want    []any
		wantErr bool
	}{
		{name: "bare scalar repaired", raw: "acct_1", schema: stringItems, want: []any{"acct_1"}},
		{name: "quoted scalar repaired", raw: `"acct_1"`, schema: stringItems, want: []any{"acct_1"}},
		{name: "bare integer repaired", raw: "5", schema: intItems, want: []any{json.Number("5")}},
		{name: "well-formed array is untouched", raw: `["a","b"]`, schema: stringItems, want: []any{"a", "b"}},
		{name: "untyped array items are not repaired", raw: "acct_1", schema: untypedArray, wantErr: true},
		{name: "object item type is not repaired", raw: "acct_1", schema: objectItems, wantErr: true},
		{name: "wrong scalar type is not repaired", raw: "not-a-number", schema: intItems, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeArgument(tc.raw, tc.schema)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeArgument(%q) = %v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeArgument(%q): %v", tc.raw, err)
			}
			gotArr, ok := got.([]any)
			if !ok || len(gotArr) != len(tc.want) {
				t.Fatalf("normalizeArgument(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
			for i := range tc.want {
				if gotArr[i] != tc.want[i] {
					t.Errorf("normalizeArgument(%q)[%d] = %#v, want %#v", tc.raw, i, gotArr[i], tc.want[i])
				}
			}
		})
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
		_, _, err := router.Finish()
		var malformed *embedded.MalformedToolCallError
		// Must be the typed error, not just a string containing "malformed":
		// Provider.Chat type-switches on it to decide EventDone versus a hard
		// EventError (see embedded.TestChatMalformedToolCallIsSoftDoneNotHardError).
		if !errors.As(err, &malformed) {
			t.Fatalf("Finish error = %T %v, want *embedded.MalformedToolCallError", err, err)
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
		// An argument that fails schema type-checking must not abort the
		// whole generation: the call is still returned, carrying the
		// problem in ArgumentsError so it reaches the model as a normal
		// tool error result and it can retry with a corrected argument.
		router := newToolOutputRouter(embedded.ToolFormatStandard, []provider.ToolSpec{weatherToolSpec()})
		router.Push(`<tool_call>{"name":"weather","arguments":{"days":"many"}}</tool_call>`)
		_, calls, err := router.Finish()
		if err != nil {
			t.Fatalf("Finish error = %v", err)
		}
		if len(calls) != 1 || calls[0].Name != "weather" || calls[0].Arguments != "" {
			t.Fatalf("calls = %+v", calls)
		}
		if !strings.Contains(calls[0].ArgumentsError, "expected a JSON number") {
			t.Fatalf("ArgumentsError = %q", calls[0].ArgumentsError)
		}
	})
}

// TestToolOutputRouterRejectsUnrepairedJSONToolBlock covers the third
// malformed-tool-call throw site (validateUnrepairedJSONToolBlocks, standard
// and Phi formats): a second <tool_call> block with invalid JSON must fail
// the whole response even though the first block parsed cleanly, and it must
// fail with the same typed error as the other two throw sites so
// Provider.Chat's soft-EventDone translation covers every tool format
// uniformly, not just Gemma.
func TestToolOutputRouterRejectsUnrepairedJSONToolBlock(t *testing.T) {
	router := newToolOutputRouter(embedded.ToolFormatStandard, []provider.ToolSpec{weatherToolSpec()})
	router.Push(`<tool_call>{"name":"weather","arguments":{"city":"Prague"}}</tool_call>` +
		`<tool_call>{"name":"weather","arguments":{city:Prague}}</tool_call>`)
	_, calls, err := router.Finish()
	var malformed *embedded.MalformedToolCallError
	if !errors.As(err, &malformed) {
		t.Fatalf("Finish returned calls %+v, err %T %v, want *embedded.MalformedToolCallError", calls, err, err)
	}
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
		// Same principle as the invalid-argument case: a missing required
		// argument is a model mistake it can fix on retry, not a reason to
		// abort the generation outright.
		required := weatherToolSpec()
		required.Parameters = []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
		router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{required})
		router.Push(`<|tool_call>call:weather{}<tool_call|>`)
		_, calls, err := router.Finish()
		if err != nil {
			t.Fatalf("Finish error = %v", err)
		}
		if len(calls) != 1 || calls[0].Name != "weather" {
			t.Fatalf("calls = %+v", calls)
		}
		if !strings.Contains(calls[0].ArgumentsError, `missing required argument "city"`) {
			t.Fatalf("ArgumentsError = %q", calls[0].ArgumentsError)
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

// TestToolOutputRouterStripsOrphanChannelTokens reproduces a leak observed
// live on Gemma 4 E4B: after a long tool-heavy conversation, the model
// repeated its entire final answer with a bare "<channel|>" token in
// between the two copies, and that literal control token reached the user.
// yzma's own StripMarkup only removes a matched
// "<|channel>thought...<channel|>" pair (Gemma's reasoning-channel
// convention); a channel opened under a different name, or — as here — an
// unpaired closing marker with no opening tag in this round's text at all,
// passes through untouched. The router must strip it defensively rather
// than show raw model-internal tokens to the user.
func TestToolOutputRouterStripsOrphanChannelTokens(t *testing.T) {
	t.Run("bare closing token between two answer chunks, streamed live", func(t *testing.T) {
		router := newToolOutputRouter(embedded.ToolFormatGemma, nil)
		var visible strings.Builder
		raw := "first answer<channel|>second answer"
		for i := range raw {
			visible.WriteString(strings.Join(router.Push(raw[i:i+1]), ""))
		}
		tail, _, err := router.Finish()
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		visible.WriteString(strings.Join(tail, ""))
		got := visible.String()
		if strings.Contains(got, "channel") {
			t.Fatalf("channel token leaked into visible text: %q", got)
		}
		if got != "first answersecond answer" {
			t.Fatalf("visible = %q, want the two chunks with the token removed", got)
		}
	})

	t.Run("named opening and closing token, streamed live", func(t *testing.T) {
		// The channel NAME ("final") is part of the token, not content: a
		// real Gemma channel directive is "<|channel>NAME ...content...
		// <channel|>", so the name is expected to be consumed along with
		// the delimiters, leaving only the space and content that followed it.
		router := newToolOutputRouter(embedded.ToolFormatGemma, nil)
		raw := "<|channel>final answer text<channel|>"
		got := strings.Join(router.Push(raw), "")
		tail, _, err := router.Finish()
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		got += strings.Join(tail, "")
		if strings.Contains(got, "channel") {
			t.Fatalf("channel token leaked into visible text: %q", got)
		}
		if strings.TrimSpace(got) != "answer text" {
			t.Fatalf("visible = %q, want the marker and name stripped", got)
		}
	})

	t.Run("token split across every possible chunk boundary", func(t *testing.T) {
		raw := "before<|channel>name mid text<channel|>after"
		for split := 0; split <= len(raw); split++ {
			router := newToolOutputRouter(embedded.ToolFormatGemma, nil)
			got := strings.Join(router.Push(raw[:split]), "") + strings.Join(router.Push(raw[split:]), "")
			tail, _, err := router.Finish()
			if err != nil {
				t.Fatalf("split %d: Finish: %v", split, err)
			}
			got += strings.Join(tail, "")
			if strings.Contains(got, "channel") {
				t.Fatalf("split %d: channel token leaked into visible text: %q", split, got)
			}
			if got != "before mid textafter" {
				t.Fatalf("split %d: visible = %q", split, got)
			}
		}
	})

	t.Run("non-Gemma formats are untouched", func(t *testing.T) {
		router := newToolOutputRouter(embedded.ToolFormatStandard, nil)
		got := strings.Join(router.Push("literal <channel|> text"), "")
		tail, _, err := router.Finish()
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		got += strings.Join(tail, "")
		if got != "literal <channel|> text" {
			t.Fatalf("visible = %q, want the non-Gemma format left untouched", got)
		}
	})
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

// TestToolOutputRouterGemmaRejectsSwallowedKey reproduces the live
// calendar_events failure: a Gemma 4 MoE finetune at temperature 0.8 omitted
// the closing quote token after "start", so yzma's parseGemmaArgs read past
// the delimiter and absorbed `, end:<|"|>...` into start's value. The call
// then reached normalizeToolCalls looking structurally valid but missing
// "end", which reported a missing required argument — sending the model to
// retry the identical call until the repeated-call guard killed the turn.
// A provably corrupt parse must be malformed instead, so the router's
// existing one-shot retry and fenced-protocol fallback can engage.
func TestToolOutputRouterGemmaRejectsSwallowedKey(t *testing.T) {
	tool := provider.ToolSpec{
		Name: "calendar_events",
		Parameters: []byte(`{
			"type":"object",
			"required":["calendar_ids","start","end","timezone"],
			"properties":{
				"calendar_ids":{"type":"array","items":{"type":"string"}},
				"start":{"type":"string"},
				"end":{"type":"string"},
				"timezone":{"type":"string"}
			}
		}`),
	}
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
	router.Push(`<|toolcall>call:calendar_events{calendar_ids:<|"|>cal_1<|"|>, ` +
		`start:<|"|>2026-09-09T00:00:00+02:00, end:<|"|>2026-09-09T23:59:59+02:00<|"|>, ` +
		`timezone:<|"|>Europe/Prague<|"|>}<toolcall|>`)
	_, calls, err := router.Finish()
	var malformed *embedded.MalformedToolCallError
	if !errors.As(err, &malformed) {
		t.Fatalf("Finish returned calls %+v, err %T %v, want *embedded.MalformedToolCallError", calls, err, err)
	}
}

// TestToolOutputRouterGemmaKeepsBracketedArrayWithMultipleElements guards the
// swallowed-key check against the shape closest to it: a bracketed array of
// quote-wrapped elements also puts a comma next to quote tokens, but that
// comma is followed by a quote token directly rather than by an identifier
// and a colon, so the check must not fire.
//
// yzma's parseGemmaArgs has no case for "[", so it truncates this value at
// the first top-level comma and loses "cal_2"; repairGemmaBracketSplitArgs
// re-parses the block and both elements must survive.
func TestToolOutputRouterGemmaKeepsBracketedArrayWithMultipleElements(t *testing.T) {
	tool := provider.ToolSpec{
		Name: "calendar_events",
		Parameters: []byte(`{
			"type":"object",
			"properties":{"calendar_ids":{"type":"array","items":{"type":"string"}}}
		}`),
	}
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
	router.Push(`<|toolcall>call:calendar_events{calendar_ids:[<|"|>cal_1<|"|>, <|"|>cal_2<|"|>]}<toolcall|>`)
	_, calls, err := router.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want the call to survive the swallowed-key check", calls)
	}
	if calls[0].Arguments != `{"calendar_ids":["cal_1","cal_2"]}` {
		t.Fatalf("arguments = %s, want both array elements", calls[0].Arguments)
	}
}

// TestToolOutputRouterGemmaRepairsBracketSplitArguments reproduces the live
// calendar_events failure against three calendars: the model emitted a
// correct call, but yzma's parseGemmaArgs has no case for "[", so it cut
// calendar_ids at the array's first inner comma and folded the rest of the
// array plus the following key into one nonsense key — making "end" look
// like an argument the model never sent. Every retry was told the same, so
// the repeated-call guard ended the turn.
func TestToolOutputRouterGemmaRepairsBracketSplitArguments(t *testing.T) {
	tool := provider.ToolSpec{
		Name: "calendar_events",
		Parameters: []byte(`{
			"type":"object",
			"required":["calendar_ids","start","end","timezone"],
			"properties":{
				"calendar_ids":{"type":"array","items":{"type":"string"}},
				"start":{"type":"string"},
				"end":{"type":"string"},
				"timezone":{"type":"string"}
			}
		}`),
	}
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
	router.Push(`<|toolcall>call:calendar_events{calendar_ids:[<|"|>cal_1<|"|>, <|"|>cal_2<|"|>, <|"|>cal_3<|"|>], ` +
		`start:<|"|>2026-09-09T00:00:00+02:00<|"|>, end:<|"|>2026-09-09T23:59:59+02:00<|"|>, ` +
		`timezone:<|"|>Europe/Prague<|"|>}<toolcall|>`)
	_, calls, err := router.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one call", calls)
	}
	if calls[0].ArgumentsError != "" {
		t.Fatalf("ArgumentsError = %q, want the call to be dispatchable", calls[0].ArgumentsError)
	}
	want := `{"calendar_ids":["cal_1","cal_2","cal_3"],"end":"2026-09-09T23:59:59+02:00",` +
		`"start":"2026-09-09T00:00:00+02:00","timezone":"Europe/Prague"}`
	if calls[0].Arguments != want {
		t.Fatalf("Arguments = %s, want %s", calls[0].Arguments, want)
	}
}

// TestToolOutputRouterGemmaRepairsRepeatedCallsPositionally guards against
// silently applying the first call's arguments to a later call with the same
// name. yzma preserves call order, but the old repair consumed a raw block
// only when a call needed repair. A clean first calendar_events call therefore
// left its block available for a bracket-split second call, which then borrowed
// the first time window instead of its own.
func TestToolOutputRouterGemmaRepairsRepeatedCallsPositionally(t *testing.T) {
	tool := provider.ToolSpec{
		Name: "calendar_events",
		Parameters: []byte(`{
			"type":"object",
			"required":["calendar_ids","start","end","timezone"],
			"properties":{
				"calendar_ids":{"type":"array","items":{"type":"string"}},
				"start":{"type":"string"},
				"end":{"type":"string"},
				"timezone":{"type":"string"}
			}
		}`),
	}
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
	router.Push(`<|toolcall>` +
		`call:calendar_events{calendar_ids:<|"|>cal_1<|"|>, ` +
		`start:<|"|>2026-09-09T08:00:00+02:00<|"|>, end:<|"|>2026-09-09T09:00:00+02:00<|"|>, ` +
		`timezone:<|"|>Europe/Prague<|"|>}` +
		`call:calendar_events{calendar_ids:[<|"|>cal_2<|"|>, <|"|>cal_3<|"|>], ` +
		`start:<|"|>2026-09-10T10:00:00+02:00<|"|>, end:<|"|>2026-09-10T11:00:00+02:00<|"|>, ` +
		`timezone:<|"|>Europe/Prague<|"|>}` +
		`<toolcall|>`)
	_, calls, err := router.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want two calls", calls)
	}
	wantFirst := `{"calendar_ids":["cal_1"],"end":"2026-09-09T09:00:00+02:00",` +
		`"start":"2026-09-09T08:00:00+02:00","timezone":"Europe/Prague"}`
	if calls[0].Arguments != wantFirst {
		t.Fatalf("first Arguments = %s, want %s", calls[0].Arguments, wantFirst)
	}
	wantSecond := `{"calendar_ids":["cal_2","cal_3"],"end":"2026-09-10T11:00:00+02:00",` +
		`"start":"2026-09-10T10:00:00+02:00","timezone":"Europe/Prague"}`
	if calls[1].Arguments != wantSecond {
		t.Fatalf("second Arguments = %s, want %s", calls[1].Arguments, wantSecond)
	}
}

func TestToolOutputRouterGemmaRepairMatchesYZMACallSelection(t *testing.T) {
	tool := provider.ToolSpec{
		Name: "calendar_events",
		Parameters: []byte(`{
			"type":"object",
			"required":["calendar_ids","start","end","timezone"],
			"properties":{
				"calendar_ids":{"type":"array","items":{"type":"string"}},
				"start":{"type":"string"},
				"end":{"type":"string"},
				"timezone":{"type":"string"}
			}
		}`),
	}

	t.Run("whitespace before opening brace", func(t *testing.T) {
		router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
		router.Push(`<|toolcall>call:calendar_events {` +
			`calendar_ids:[<|"|>cal_1<|"|>, <|"|>cal_2<|"|>], ` +
			`start:<|"|>2026-09-09T08:00:00+02:00<|"|>, end:<|"|>2026-09-09T09:00:00+02:00<|"|>, ` +
			`timezone:<|"|>Europe/Prague<|"|>}<toolcall|>`)
		_, calls, err := router.Finish()
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if len(calls) != 1 || !strings.Contains(calls[0].Arguments, `"calendar_ids":["cal_1","cal_2"]`) {
			t.Fatalf("calls = %+v, want repaired calendar ids", calls)
		}
	})

	t.Run("mixed empty and populated calls", func(t *testing.T) {
		router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
		router.Push(`<|toolcall>` +
			`call:calendar_events{calendar_ids:<|"|>cal_1<|"|>, ` +
			`start:<|"|>2026-09-09T08:00:00+02:00<|"|>, end:<|"|>2026-09-09T09:00:00+02:00<|"|>, ` +
			`timezone:<|"|>Europe/Prague<|"|>}` +
			`call:calendar_events{}` +
			`call:calendar_events{calendar_ids:[<|"|>cal_2<|"|>, <|"|>cal_3<|"|>], ` +
			`start:<|"|>2026-09-10T10:00:00+02:00<|"|>, end:<|"|>2026-09-10T11:00:00+02:00<|"|>, ` +
			`timezone:<|"|>Europe/Prague<|"|>}` +
			`<toolcall|>`)
		_, calls, err := router.Finish()
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if len(calls) != 2 {
			t.Fatalf("calls = %+v, want two populated calls", calls)
		}
		if !strings.Contains(calls[1].Arguments, `"calendar_ids":["cal_2","cal_3"]`) ||
			!strings.Contains(calls[1].Arguments, `"start":"2026-09-10T10:00:00+02:00"`) {
			t.Fatalf("second call = %+v, want its own repaired arguments", calls[1])
		}
	})
}

// TestToolOutputRouterGemmaRejectsImpossibleKey covers the residue shapes the
// bracket-aware re-parse cannot recover: whatever survives must never be
// dispatched or reported as a missing argument, since the absorbed key is not
// one the model declined to send.
func TestToolOutputRouterGemmaRejectsImpossibleKey(t *testing.T) {
	tool := provider.ToolSpec{
		Name:       "calendar_events",
		Parameters: []byte(`{"type":"object","properties":{"start":{"type":"string"}}}`),
	}
	router := newToolOutputRouter(embedded.ToolFormatGemma, []provider.ToolSpec{tool})
	router.Push(`<|toolcall>call:calendar_events{<|"|>x<|"|>], start:<|"|>2026-09-09T00:00:00+02:00<|"|>}<toolcall|>`)
	_, calls, err := router.Finish()
	var malformed *embedded.MalformedToolCallError
	if !errors.As(err, &malformed) {
		t.Fatalf("Finish returned calls %+v, err %T %v, want *embedded.MalformedToolCallError", calls, err, err)
	}
}
