package agentverify

import (
	"strings"
	"testing"
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
