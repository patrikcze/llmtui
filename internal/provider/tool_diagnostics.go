package provider

import (
	"strings"
)

// ToolCallStage names an observed boundary in the native tool-call path. It
// is diagnostic metadata only: no value of this type is ever used to create a
// ToolCall or to decide whether a tool may run.
type ToolCallStage string

const (
	ToolCallStageRequestPrepared    ToolCallStage = "request_prepared"
	ToolCallStageResponseReceived   ToolCallStage = "response_received"
	ToolCallStageIntentSuspected    ToolCallStage = "intent_suspected"
	ToolCallStageProviderDecoded    ToolCallStage = "provider_decoded"
	ToolCallStageNormalized         ToolCallStage = "normalized"
	ToolCallStageToolResolved       ToolCallStage = "tool_resolved"
	ToolCallStageArgumentsInvalid   ToolCallStage = "arguments_invalid"
	ToolCallStageApprovalRequired   ToolCallStage = "approval_required"
	ToolCallStageApprovalDenied     ToolCallStage = "approval_denied"
	ToolCallStageExecutionStarted   ToolCallStage = "execution_started"
	ToolCallStageExecutionSucceeded ToolCallStage = "execution_succeeded"
	ToolCallStageExecutionFailed    ToolCallStage = "execution_failed"
	ToolCallStageResultCorrelated   ToolCallStage = "result_correlated"
)

// ToolCallClassification is the outcome observed at one stage. Classifications
// intentionally describe evidence rather than intent: an empty native-call
// array cannot prove that a model declined to use a tool.
type ToolCallClassification string

const (
	ToolCallNoIntentObserved       ToolCallClassification = "no_tool_intent_observed"
	ToolCallNativeReceived         ToolCallClassification = "native_tool_call_received"
	ToolCallSuspectedCensored      ToolCallClassification = "suspected_censored_tool_call"
	ToolCallIncompleteStream       ToolCallClassification = "incomplete_streamed_tool_call"
	ToolCallProviderParseError     ToolCallClassification = "provider_parse_error"
	ToolCallNormalizationError     ToolCallClassification = "normalization_error"
	ToolCallUnknownTool            ToolCallClassification = "unknown_tool"
	ToolCallInvalidArguments       ToolCallClassification = "invalid_arguments"
	ToolCallApprovalBlocked        ToolCallClassification = "approval_blocked"
	ToolCallExecutionFailed        ToolCallClassification = "execution_failure"
	ToolCallResultCorrelationError ToolCallClassification = "result_correlation_failure"
)

// ToolCallDiagnostic is a bounded, content-free observation. Detail is a
// stable category/marker name, never a provider response, tool arguments,
// headers, or hidden reasoning. It is retained only in the current TUI
// process for /debug last.
type ToolCallDiagnostic struct {
	Stage           ToolCallStage
	Classification  ToolCallClassification
	ToolCallID      string
	ToolName        string
	NativeCallCount int
	Streaming       bool
	Detail          string
}

// ObserveToolCallResponse records the provider-facing part of one response.
// content is inspected solely for a small, provider-aware set of control
// envelopes. This function deliberately never parses arguments, returns a
// ToolCall, or attempts recovery. Callers must continue to use their normal
// structured decoder and validation path.
func ObserveToolCallResponse(model string, streaming bool, content string, calls []ToolCall, truncated, malformed bool) []ToolCallDiagnostic {
	diagnostics := []ToolCallDiagnostic{{
		Stage: ToolCallStageResponseReceived, Streaming: streaming,
		NativeCallCount: len(calls),
	}}
	if len(calls) > 0 {
		for _, call := range calls {
			diagnostics = append(diagnostics, ToolCallDiagnostic{
				Stage: ToolCallStageProviderDecoded, Classification: ToolCallNativeReceived,
				ToolCallID: call.ID, ToolName: call.Name, NativeCallCount: len(calls), Streaming: streaming,
			})
		}
		return diagnostics
	}
	if malformed {
		return append(diagnostics, ToolCallDiagnostic{
			Stage: ToolCallStageIntentSuspected, Classification: ToolCallProviderParseError,
			Streaming: streaming, Detail: "provider_reported_malformed_native_call",
		})
	}
	if marker := suspectedToolMarker(model, content); marker != "" {
		classification := ToolCallSuspectedCensored
		if truncated {
			classification = ToolCallIncompleteStream
		}
		return append(diagnostics, ToolCallDiagnostic{
			Stage: ToolCallStageIntentSuspected, Classification: classification,
			Streaming: streaming, Detail: marker,
		})
	}
	return append(diagnostics, ToolCallDiagnostic{
		Stage: ToolCallStageIntentSuspected, Classification: ToolCallNoIntentObserved,
		Streaming: streaming,
	})
}

// suspectedToolMarker accepts only control-envelope prefixes produced by
// known provider families. Ordinary prose, fenced examples, and bare JSON
// intentionally do not qualify. It does not inspect reasoning channels.
func suspectedToolMarker(model, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	protocol := ResolveModelProtocol(model, "")
	if protocol.HarmonyRequired && (strings.HasPrefix(trimmed, "to=functions.") || strings.HasPrefix(trimmed, "to=functions ") || strings.HasPrefix(trimmed, "<|channel>")) {
		return "harmony_recipient_or_channel"
	}
	switch {
	case strings.HasPrefix(trimmed, "<tool_call>"), strings.HasPrefix(trimmed, "<|tool_call>"), strings.HasPrefix(trimmed, "<|toolcall>"), strings.HasPrefix(trimmed, "<|tools>"):
		return "tool_call_envelope"
	case strings.HasPrefix(trimmed, "<function="):
		return "qwen_function_envelope"
	case strings.HasPrefix(trimmed, "[TOOL_CALLS]"):
		return "mistral_tool_calls_envelope"
	}
	return ""
}
