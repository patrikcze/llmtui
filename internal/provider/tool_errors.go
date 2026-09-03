package provider

import (
	"fmt"
	"strings"
)

// ToolNotOfferedError reports a structured tool call whose exact name was not
// present in the provider request's tool snapshot. Controllers may inspect it
// to offer a safer recovery, but the rejected call itself must never execute.
type ToolNotOfferedError struct {
	RequestedName string
	OfferedNames  []string
}

func (e *ToolNotOfferedError) Error() string {
	if e == nil {
		return "model requested a tool that was not offered"
	}
	return fmt.Sprintf(
		"model requested unknown tool %q; offered tools: %s",
		e.RequestedName,
		strings.Join(e.OfferedNames, ", "),
	)
}
