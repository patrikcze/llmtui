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
