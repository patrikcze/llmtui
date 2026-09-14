package agentverify

import (
	"context"
	"strings"
	"testing"
	"time"

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
