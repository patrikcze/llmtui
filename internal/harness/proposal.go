package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/untrusted"
)

const maxProposalBytes = 8192

// Proposal explanations are checked but never persisted or injected as guidance.
type Proposal struct {
	Scope     string `json:"scope"`
	Failure   string `json:"failure"`
	Evidence  string `json:"evidence"`
	Edit      Edit   `json:"edit"`
	Component string `json:"component"`
	Expected  string `json:"expected"`
	Risks     string `json:"risks"`
	Objective string `json:"objective"`
}

func decodeStrict(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errors.New("invalid control JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing control JSON")
	}
	// encoding/json otherwise accepts duplicate keys, including nested ones.
	return rejectDuplicateKeys(data)
}

func rejectDuplicateKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate control JSON key")
				}
				seen[name] = true
			}
			if err := walk(); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return walk()
}

func ParseProposal(data []byte, scope string, baseline Evidence) (Proposal, error) {
	var p Proposal
	if len(data) > maxProposalBytes {
		return p, errors.New("proposal size budget exceeded")
	}
	if err := decodeStrict(data, &p); err != nil {
		return Proposal{}, err
	}
	g, ok := Catalog()[p.Edit]
	if !ok || g.Scope != scope || p.Scope != scope || p.Component != g.Component {
		return Proposal{}, errors.New("proposal requests unsupported scope or protected component")
	}
	for _, v := range []string{p.Failure, p.Evidence, p.Expected, p.Risks, p.Objective} {
		if strings.TrimSpace(v) == "" || len(v) > 512 {
			return Proposal{}, errors.New("missing or oversized proposal rationale")
		}
	}
	if p.Evidence != baseline.ID || baseline.Passed {
		return Proposal{}, errors.New("proposal lacks baseline failure evidence")
	}
	return p, nil
}

// proposalConstraint constrains remote JSON Schema and embedded GBNF through
// the existing provider contract. Decoding still validates every field.
func proposalConstraint(scope string) *provider.ResponseConstraint {
	keys := []string{"scope", "failure", "evidence", "edit", "component", "expected", "risks", "objective"}
	properties := map[string]any{}
	grammar := "root ::= \"{\" ws "
	for i, key := range keys {
		properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": 512}
		if i > 0 {
			grammar += "\",\" ws "
		}
		grammar += strconv.Quote(strconv.Quote(key)) + " ws \":\" ws string "
	}
	grammar += `"}" ws
string ::= "\"" ([^"\\] | "\\" (["\\/bfnrt] | "u" [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F]))* "\"" ws
ws ::= [ \t\n\r]*`
	properties["scope"] = map[string]any{"type": "string", "enum": []string{scope}}
	edits := []Edit{}
	for edit, g := range Catalog() {
		if g.Scope == scope {
			edits = append(edits, edit)
		}
	}
	slices.Sort(edits)
	properties["edit"] = map[string]any{"type": "string", "enum": edits}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": keys, "additionalProperties": false})
	return &provider.ResponseConstraint{Name: "llmtui_harness_proposal", Strict: true, JSONSchema: schema, Grammar: grammar, GrammarRoot: "root"}
}

// Proposer reuses any configured backend's Chat API, without tools or a new loop.
// There are exactly two requests per experiment and no format-repair retries.
type Proposer struct {
	Provider    provider.Provider
	Model       string
	Temperature float64
	TopP        float64
	MaxTokens   int
}

func (p Proposer) propose(ctx context.Context, scope string, baseline Evidence, lessons []Experience) (Proposal, int, error) {
	if p.Provider == nil || p.Model == "" || p.MaxTokens < 128 || p.MaxTokens > 2048 {
		return Proposal{}, 0, errors.New("invalid proposer configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	catalog, _ := json.Marshal(Catalog())
	evidence, _ := json.Marshal(baseline)
	experience, _ := json.Marshal(lessons)
	instruction := `Select exactly one catalog edit for the earliest demonstrated failure. Return only a JSON object with string fields scope, failure, evidence, edit, component, expected, risks, objective. Evidence must be the baseline evidence ID. Objective must be falsifiable. Explain the failure and risks briefly. Never propose permissions, security rules, scripts, plugins or free-text instructions. Scope: ` + scope + "\nCatalog:\n" + string(catalog)
	req := provider.ChatRequest{Isolated: true, Model: p.Model, Temperature: p.Temperature, TopP: p.TopP, MaxTokens: p.MaxTokens, Stream: true, Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: instruction},
		{Role: provider.RoleUser, Content: untrusted.Frame("harness_evidence", "fixture", string(evidence)) + "\n" + untrusted.Frame("harness_experience", "simulation-only", string(experience))},
	}}
	caps := provider.DefaultCapabilities()
	if reporter, ok := p.Provider.(provider.ModelCapabilityReporter); ok {
		caps = reporter.CapabilitiesFor(p.Model)
	} else if reporter, ok := p.Provider.(provider.CapabilityReporter); ok {
		caps = reporter.Capabilities()
	}
	if caps.StructuredOutput == provider.CapabilitySupported {
		req.ResponseConstraint = proposalConstraint(scope)
	}
	// Strict decoding remains authoritative when constraints are unavailable.
	// Unsupported constraints reject this proposal and preserve the baseline.
	// No extra provider retry or fallback tool parser is introduced.
	inputTokens := provider.EstimateMessageTokens(req.Messages[0]) + provider.EstimateMessageTokens(req.Messages[1])
	if inputTokens+p.MaxTokens > maxProposalRequestTokens {
		return Proposal{}, 0, errors.New("proposal request token budget exceeded")
	}
	events, err := p.Provider.Chat(ctx, req)
	if err != nil {
		return Proposal{}, 0, errors.New("harness proposal provider request failed")
	}
	var b strings.Builder
	tokens := inputTokens
	responseBytes := 0
	for {
		select {
		case <-ctx.Done():
			return Proposal{}, tokens, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return Proposal{}, tokens, errors.New("proposal stream ended without completion")
			}
			activityBytes := max(len(ev.Delta), ev.ReasoningBytes)
			if activityBytes > maxProposalBytes-responseBytes || ev.ReasoningBytes < 0 {
				return Proposal{}, tokens, errors.New("proposal response budget exceeded")
			}
			responseBytes += activityBytes
			tokens = inputTokens + (responseBytes+3)/4
			switch ev.Type {
			case provider.EventDelta:
				if b.Len()+len(ev.Delta) > maxProposalBytes {
					return Proposal{}, tokens, errors.New("proposal size budget exceeded")
				}
				b.WriteString(ev.Delta)
			case provider.EventError:
				return Proposal{}, tokens, errors.New("harness proposal provider stream failed")
			case provider.EventDone:
				if err := ctx.Err(); err != nil {
					return Proposal{}, tokens, err
				}
				if ev.Truncated || ev.MalformedToolCall || len(ev.ToolCalls) > 0 || (ev.Turn != nil && len(ev.Turn.ToolCalls) > 0) {
					return Proposal{}, tokens, errors.New("proposal returned incomplete output or a tool request")
				}
				if b.Len() == 0 && ev.Turn != nil {
					if len(ev.Turn.FinalContent) > maxProposalBytes {
						return Proposal{}, tokens, errors.New("proposal size budget exceeded")
					}
					b.WriteString(ev.Turn.FinalContent)
				}
				tokens = max(tokens, inputTokens+provider.EstimateTokens(b.String()))
				if ev.Usage != nil {
					tokens = max(tokens, ev.Usage.PromptTokens+ev.Usage.CompletionTokens)
				}
				out, err := ParseProposal([]byte(b.String()), scope, baseline)
				if err != nil {
					return Proposal{}, tokens, fmt.Errorf("reject proposal: %w", err)
				}
				return out, tokens, nil
			}
		}
	}
}
