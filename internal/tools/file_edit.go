package tools

import (
	"fmt"
	"strings"
)

// MaxEditFilePayloadBytes bounds the JSON body of a fenced edit_file block
// (old_text + new_text + framing). A genuinely larger change is a whole-file
// replacement — that is write_file's job.
const MaxEditFilePayloadBytes = 512 * 1024

// maxReadFilePayloadBytes bounds the tiny JSON body of a fenced ranged
// read_file block ({"offset":N,"limit":M}).
const maxReadFilePayloadBytes = 512

type readFileArgs struct {
	Offset int `json:"offset,omitempty"`
	Limit  int `json:"limit,omitempty"`
}

type editFileArgs struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// decodeReadFileBody parses the optional JSON range object from a fenced
// read_file block. The legacy form — path in the info string, empty body — is
// left untouched, and so is a non-JSON body (a model that mistakenly pasted
// content there still gets the old "body ignored" behavior rather than a new
// hard error).
func decodeReadFileBody(call *Call) {
	body := strings.TrimSpace(call.Body)
	if !strings.HasPrefix(body, "{") {
		return
	}
	if len(call.Body) > maxReadFilePayloadBytes {
		call.InputErr = fmt.Sprintf("read_file range arguments exceed the %d byte limit", maxReadFilePayloadBytes)
		return
	}
	var args readFileArgs
	if err := decodeOneJSONObject(call.Body, &args); err != nil {
		call.InputErr = "read_file range needs one JSON object like {\"offset\":1,\"limit\":200}: " + err.Error()
		return
	}
	if err := ValidateReadRange(args.Offset, args.Limit); err != nil {
		call.InputErr = err.Error()
		return
	}
	call.Offset, call.Limit = args.Offset, args.Limit
	call.Body = ""
}

// decodeEditFileBody parses the {"old_text":…,"new_text":…} object from a
// fenced edit_file block. The target path stays in the info string, matching
// write_file.
func decodeEditFileBody(call *Call) {
	if len(call.Body) > MaxEditFilePayloadBytes {
		call.InputErr = fmt.Sprintf("edit_file arguments exceed the %d byte limit", MaxEditFilePayloadBytes)
		return
	}
	var args editFileArgs
	if err := decodeOneJSONObject(call.Body, &args); err != nil {
		call.InputErr = "edit_file needs one JSON object with old_text and new_text in the block body: " + err.Error()
		return
	}
	call.OldText, call.NewText = args.OldText, args.NewText
	call.Body = ""
	if err := ValidateEditFileCall(call); err != nil {
		call.InputErr = err.Error()
	}
}

// ValidateEditFileCall applies the model-facing prec: a non-empty target
// path, a non-empty old_text, and a change that actually changes something.
// It grants nothing and performs no I/O.
func ValidateEditFileCall(call *Call) error {
	if call == nil {
		return fmt.Errorf("edit_file call is missing")
	}
	if strings.TrimSpace(call.Path) == "" {
		return fmt.Errorf("edit_file needs a target path in the block info string")
	}
	if call.OldText == "" {
		return fmt.Errorf("edit_file needs old_text — the exact fragment to replace")
	}
	if call.OldText == call.NewText {
		return fmt.Errorf("edit_file old_text and new_text are identical; nothing to change")
	}
	return nil
}
