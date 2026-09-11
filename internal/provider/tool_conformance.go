package provider

import (
	"context"
	"encoding/json"
	"fmt"
)

// ConformanceStatus is one observed result of the diagnostic-only native tool
// probe. It deliberately distinguishes not tested from failure.
type ConformanceStatus string

const (
	ConformancePass         ConformanceStatus = "pass"
	ConformanceFail         ConformanceStatus = "fail"
	ConformanceNotTested    ConformanceStatus = "not_tested"
	ConformanceNotSupported ConformanceStatus = "not_supported"
)

// ToolCallConformance reports one narrowly scoped observation for a provider,
// model, and request shape. The probe never invokes a host tool; its only
// follow-up is a synthetic role:"tool" message after a native call arrives.
type ToolCallConformance struct {
	AdvertisedCapability CapabilitySupport
	NativeCall           ConformanceStatus
	ToolName             ConformanceStatus
	Arguments            ConformanceStatus
	CallID               ConformanceStatus
	Streaming            ConformanceStatus
	ResultCorrelation    ConformanceStatus
	RequiredSelection    ConformanceStatus
	SuspectedCensoring   bool
	FailureBoundary      ToolCallStage
	Detail               string
}

const conformanceProbeToken = "llmtui-tool-conformance-v1"

// ConformanceEchoTool is the harmless, strict tool declaration used by
// ProbeNativeToolCalls. It is deliberately not part of the ordinary tool
// registry and is never routed through an executor.
func ConformanceEchoTool() ToolSpec {
	return ToolSpec{
		Name: "conformance_echo", Description: "Return probe_token unchanged.", Strict: true,
		Parameters: json.RawMessage(`{"type":"object","properties":{"probe_token":{"type":"string"}},"required":["probe_token"],"additionalProperties":false}`),
	}
}

// ProbeNativeToolCalls samples the existing provider contract without giving
// a synthetic call to the application tool runner. The common ChatRequest has
// no tool_choice/required field, so RequiredSelection is explicitly reported
// as not supported rather than faking an assertion the provider cannot make.
func ProbeNativeToolCalls(ctx context.Context, p Provider, model string, streaming bool) (ToolCallConformance, error) {
	report := ToolCallConformance{
		NativeCall: ConformanceFail, ToolName: ConformanceNotTested, Arguments: ConformanceNotTested,
		CallID: ConformanceNotTested, Streaming: ConformanceNotTested, ResultCorrelation: ConformanceNotTested,
		RequiredSelection: ConformanceNotSupported, FailureBoundary: ToolCallStageProviderDecoded,
	}
	report.AdvertisedCapability = CapabilitiesFor(p, model).NativeTools
	request := ChatRequest{
		Model: model, Stream: streaming, Temperature: 0, MaxTokens: 128, Tools: []ToolSpec{ConformanceEchoTool()},
		Messages: []Message{{Role: RoleUser, Content: "Call conformance_echo exactly once with probe_token set to \"" + conformanceProbeToken + "\". Do not answer with text."}},
	}
	done, err := awaitProbeDone(ctx, p, request)
	if err != nil {
		return report, err
	}
	for _, event := range done.ToolCallDiagnostics {
		if event.Classification == ToolCallSuspectedCensored || event.Classification == ToolCallIncompleteStream || event.Classification == ToolCallProviderParseError {
			report.SuspectedCensoring = true
			report.FailureBoundary = event.Stage
			report.Detail = event.Detail
		}
	}
	if len(done.ToolCalls) != 1 {
		if report.SuspectedCensoring {
			report.Detail = "provider returned no usable native call; " + report.Detail
		} else {
			report.Detail = fmt.Sprintf("provider returned %d native calls", len(done.ToolCalls))
		}
		return report, nil
	}
	call := done.ToolCalls[0]
	report.NativeCall, report.Streaming = ConformancePass, ConformancePass
	if call.Name != "conformance_echo" {
		report.ToolName, report.Detail = ConformanceFail, "unexpected tool name"
		return report, nil
	}
	report.ToolName = ConformancePass
	if call.ID == "" {
		report.CallID, report.Detail = ConformanceFail, "provider omitted tool-call ID"
	} else {
		report.CallID = ConformancePass
	}
	var args struct {
		ProbeToken string `json:"probe_token"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil || args.ProbeToken != conformanceProbeToken {
		report.Arguments, report.Detail = ConformanceFail, "probe arguments were not preserved"
		return report, nil
	}
	report.Arguments = ConformancePass

	// A role:"tool" continuation checks that the provider accepts a result
	// correlated with the same native ID. It is not host execution.
	followup := request
	followup.Messages = append(followup.Messages,
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		Message{Role: RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: `{"probe_token":"` + conformanceProbeToken + `"}`},
	)
	if _, err := awaitProbeDone(ctx, p, followup); err != nil {
		report.ResultCorrelation, report.Detail = ConformanceFail, "provider rejected correlated tool result"
		return report, nil
	}
	report.ResultCorrelation = ConformancePass
	return report, nil
}

func awaitProbeDone(ctx context.Context, p Provider, request ChatRequest) (ChatEvent, error) {
	events, err := p.Chat(ctx, request)
	if err != nil {
		return ChatEvent{}, err
	}
	for {
		select {
		case <-ctx.Done():
			return ChatEvent{}, ctx.Err()
		case event, ok := <-events:
			if !ok {
				return ChatEvent{}, fmt.Errorf("provider stream closed without EventDone")
			}
			switch event.Type {
			case EventDone:
				return event, nil
			case EventError:
				return ChatEvent{}, event.Err
			}
		}
	}
}
