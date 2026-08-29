package tui

import "github.com/patrikcze/llmtui/internal/provider"

// pickerState groups the arrow-key picker overlay fields. They are always set
// together when a picker opens (model / provider / profile / skill / plugin /
// agent-question / agent-promotion) and cleared together by clearPicker. The
// pickerKind enum and its constants stay in commands.go.
type pickerState struct {
	pickerKind   pickerKind
	pickerItems  []string
	pickerIdx    int
	pickerModels []provider.ModelInfo
	// pickerHeader is the executor's actual question text, shown above the
	// options list when pickerKind == pickerAgentQuestion. Unused by every
	// other picker kind.
	pickerHeader string
}
