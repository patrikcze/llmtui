package agentverify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
)

func TestContractPromptUsesSuppliedClarification(t *testing.T) {
	messages := contractMessages(`{"task":"Read the file I mentioned","user_input":"report.md"}`)
	if len(messages) == 0 || !strings.Contains(messages[0].Content, "do not ask that same question again") {
		t.Fatalf("contract prompt must direct the model to use a supplied clarification: %+v", messages)
	}
}

func TestContractPromptTreatsNamedFileAsSufficientInput(t *testing.T) {
	messages := contractMessages(`{"task":"Read absent.md and summarize it."}`)
	if len(messages) == 0 || !strings.Contains(messages[0].Content, "literal file path") {
		t.Fatalf("contract prompt must tell the model to attempt a named file: %+v", messages)
	}
}

func TestContractPromptRequiresEveryExplicitDeliverable(t *testing.T) {
	messages := contractMessages(`{"task":"Read report.md and give me its heading."}`)
	if len(messages) == 0 || !strings.Contains(messages[0].Content, "Every explicit deliverable") {
		t.Fatalf("contract prompt must require complete criteria: %+v", messages)
	}
}

// TestContractPromptReferencesCapabilityCapsule proves the Phase 4 capsule
// addition is actually instructed, not just carried silently in the JSON
// payload: the prompt must tell the model never to paste file contents when
// a read capability is available, and never to invent criteria for a
// category the capsule marks unavailable.
func TestContractPromptReferencesCapabilityCapsule(t *testing.T) {
	messages := contractMessages(`{"task":"summarize report.md","capabilities":{"workspace_access":true,"available":["read_files"]}}`)
	if len(messages) == 0 {
		t.Fatal("no messages")
	}
	for _, want := range []string{"never ask the user to paste file contents", "do not silently invent criteria"} {
		if !strings.Contains(messages[0].Content, want) {
			t.Fatalf("contract prompt missing capsule instruction %q: %s", want, messages[0].Content)
		}
	}
}

// TestContractUsesSelectedModelCapabilitiesNotProviderWide mirrors
// TestVerifierUsesSelectedModelCapabilitiesNotProviderWide for
// EstablishContract: the same finding #6 gap (provider-wide capability
// report used instead of the selected model's) existed here too.
func TestContractUsesSelectedModelCapabilitiesNotProviderWide(t *testing.T) {
	client := &modelCapabilityClient{
		recordingClient: recordingClient{
			reply: `{"criteria":["a criterion"],"needs_user_input":false,"question":"","user_options":[]}`,
			caps:  provider.Capabilities{StructuredOutput: provider.CapabilitySupported},
		},
		perModel: map[string]provider.Capabilities{
			"weak-model": {StructuredOutput: provider.CapabilityUnsupported},
		},
	}
	if _, err := EstablishContract(context.Background(), client, Config{Model: "weak-model", Timeout: time.Second}, ContractInput{Task: "do something"}); err != nil {
		t.Fatal(err)
	}
	if constraint := client.requests[0].ResponseConstraint; constraint != nil {
		t.Fatalf("weak-model constraint = %+v, want none", constraint)
	}
}

func TestParseContractOptionalAssessmentsIsAllOrNothing(t *testing.T) {
	valid := `{"criteria":["read report.md","report the heading"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":0,"version":1,"proposition":"the read receipt supports the criterion","evidence_kind":"local_read","target":"report.md"},{"criterion_index":1,"version":1,"proposition":"the receipt supports the report","evidence_kind":"receipts"}]}`
	contract, err := ParseContract(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(contract.Assessments) != 2 || contract.Assessments[0].Target != "report.md" {
		t.Fatalf("assessments = %+v", contract.Assessments)
	}

	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "duplicate index",
			raw:  `{"criteria":["first"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":0,"version":1,"proposition":"one","evidence_kind":"receipts"},{"criterion_index":0,"version":1,"proposition":"two","evidence_kind":"receipts"}]}`,
		},
		{
			name: "out of range",
			raw:  `{"criteria":["first"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":1,"version":1,"proposition":"one","evidence_kind":"receipts"}]}`,
		},
		{
			name: "unsupported version",
			raw:  `{"criteria":["first"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":0,"version":2,"proposition":"one","evidence_kind":"receipts"}]}`,
		},
		{
			name: "missing required field",
			raw:  `{"criteria":["first"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":0,"proposition":"one","evidence_kind":"receipts"}]}`,
		},
		{
			name: "unsafe target",
			raw:  `{"criteria":["first"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":0,"version":1,"proposition":"one","evidence_kind":"local_read","target":"../secret"}]}`,
		},
		{
			name: "overlong proposition",
			raw:  `{"criteria":["first"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":0,"version":1,"proposition":"` + strings.Repeat("x", 257) + `","evidence_kind":"receipts"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract, err := ParseContract(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(contract.Criteria) != 1 || len(contract.Assessments) != 0 || contract.AssessmentDiscardReason == "" {
				t.Fatalf("contract = %+v, want valid core and discarded extension", contract)
			}
		})
	}

	clarification, err := ParseContract(`{"criteria":["provisional"],"needs_user_input":true,"question":"Which file?","user_options":[],"assessments":[{"criterion_index":0,"version":1,"proposition":"guess","evidence_kind":"receipts"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if clarification.Assessments != nil || clarification.AssessmentDiscardReason != "clarification_contract" {
		t.Fatalf("clarification assessments = %+v reason=%q", clarification.Assessments, clarification.AssessmentDiscardReason)
	}
}

func TestEstablishContractAssessmentExtensionIsOptIn(t *testing.T) {
	reply := `{"criteria":["read report.md"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":0,"version":1,"proposition":"the read supports the criterion","evidence_kind":"local_read","target":"report.md"}]}`
	client := &recordingClient{
		reply: reply,
		caps:  provider.Capabilities{StructuredOutput: provider.CapabilitySupported},
	}
	out, err := EstablishContract(context.Background(), client, Config{Model: "local", Timeout: time.Second}, ContractInput{
		Task: "read report.md", AssessmentVersion: agent.CriterionAssessmentVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Contract.Assessments) != 1 {
		t.Fatalf("assessment contract = %+v", out.Contract)
	}
	request := client.requests[0]
	if request.ResponseConstraint == nil || !strings.Contains(string(request.ResponseConstraint.JSONSchema), `"assessments"`) {
		t.Fatalf("assessment schema = %+v", request.ResponseConstraint)
	}
	if !strings.Contains(request.Messages[0].Content, "For evaluation only") {
		t.Fatal("assessment prompt extension missing")
	}

	legacyClient := &recordingClient{
		reply: reply,
		caps:  provider.Capabilities{StructuredOutput: provider.CapabilitySupported},
	}
	legacy, err := EstablishContract(context.Background(), legacyClient, Config{Model: "local", Timeout: time.Second}, ContractInput{Task: "read report.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.Contract.Assessments) != 0 || strings.Contains(string(legacyClient.requests[0].ResponseConstraint.JSONSchema), `"assessments"`) {
		t.Fatalf("legacy contract unexpectedly enabled assessments: %+v", legacy)
	}
}

func TestEstablishContractInvalidOptionalAssessmentDoesNotRepairCoreContract(t *testing.T) {
	client := &recordingClient{
		reply: `{"criteria":["read report.md"],"needs_user_input":false,"question":"","user_options":[],"assessments":[{"criterion_index":3,"version":1,"proposition":"orphan","evidence_kind":"receipts"}]}`,
	}
	out, err := EstablishContract(context.Background(), client, Config{Model: "local", Timeout: time.Second}, ContractInput{
		Task: "read report.md", AssessmentVersion: agent.CriterionAssessmentVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || len(out.Contract.Criteria) != 1 || out.Contract.AssessmentDiscardReason == "" {
		t.Fatalf("requests=%d contract=%+v, want one request and valid core", len(client.requests), out.Contract)
	}
}
