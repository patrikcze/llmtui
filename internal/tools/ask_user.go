package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxAskUserPayloadBytes  = 8 * 1024
	MaxAskUserQuestionRunes = 1024
	MaxAskUserChoices       = 4
	MaxAskUserChoiceRunes   = 256
)

type askUserArgs struct {
	Question  string   `json:"question"`
	Choices   []string `json:"choices,omitempty"`
	AllowText bool     `json:"allow_text,omitempty"`
}

func decodeAskUserBody(call *Call) {
	if len(call.Body) > MaxAskUserPayloadBytes {
		call.InputErr = fmt.Sprintf("ask_user arguments exceed the %d byte limit", MaxAskUserPayloadBytes)
		return
	}
	var args askUserArgs
	decoder := json.NewDecoder(strings.NewReader(call.Body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		call.InputErr = "ask_user needs one JSON object in the tool block body: " + err.Error()
		return
	}
	call.Question = args.Question
	call.setAskUserChoices(args.Choices)
	call.AllowText = args.AllowText
	if err := ValidateAskUserCall(call); err != nil {
		call.InputErr = err.Error()
	}
}

// ValidateAskUserCall normalizes and bounds one model-authored question.
// It grants no permission and must run before the controller pauses a turn.
func ValidateAskUserCall(call *Call) error {
	if call == nil {
		return fmt.Errorf("ask_user call is missing")
	}
	call.Question = strings.TrimSpace(call.Question)
	if call.Question == "" {
		return fmt.Errorf("ask_user needs a non-empty question")
	}
	if utf8.RuneCountInString(call.Question) > MaxAskUserQuestionRunes {
		return fmt.Errorf("ask_user question exceeds %d characters", MaxAskUserQuestionRunes)
	}
	if call.ChoiceCount > MaxAskUserChoices {
		return fmt.Errorf("ask_user accepts at most %d choices", MaxAskUserChoices)
	}
	for index := 0; index < call.ChoiceCount; index++ {
		call.Choices[index] = strings.TrimSpace(call.Choices[index])
		if call.Choices[index] == "" {
			return fmt.Errorf("ask_user choice %d is blank", index+1)
		}
		if utf8.RuneCountInString(call.Choices[index]) > MaxAskUserChoiceRunes {
			return fmt.Errorf("ask_user choice %d exceeds %d characters", index+1, MaxAskUserChoiceRunes)
		}
	}
	if call.ChoiceCount == 0 {
		call.AllowText = true
	}
	return nil
}

func (call *Call) setAskUserChoices(choices []string) {
	call.ChoiceCount = len(choices)
	for index := 0; index < min(len(choices), MaxAskUserChoices); index++ {
		call.Choices[index] = choices[index]
	}
}

// AskUserChoices returns a copy of the validated choices in model order.
func (call Call) AskUserChoices() []string {
	count := min(max(call.ChoiceCount, 0), MaxAskUserChoices)
	return append([]string(nil), call.Choices[:count]...)
}
