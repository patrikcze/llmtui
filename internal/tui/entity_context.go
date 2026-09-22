package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/untrusted"
)

const defaultEntityContextTokens = 1200

func (m *Model) entitiesEnabled() bool {
	return m.cfg != nil && m.cfg.Entities.Enabled && m.entities != nil
}

func (m *Model) resetEntities() {
	if m.entities != nil {
		m.entities.Reset()
	}
	m.resetVisionObservations()
}

func (m *Model) entityPromptRecords() []prompt.EntityRecord {
	if !m.entityToolsAvailable() {
		return nil
	}
	maxBytes := m.entityContextTokenBudget()
	if maxBytes <= int(^uint(0)>>2) {
		maxBytes *= 4
	} else {
		maxBytes = int(^uint(0) >> 1)
	}
	views := m.entities.MinimalViews(maxBytes)
	records := make([]prompt.EntityRecord, 0, len(views))
	for _, view := range views {
		records = append(records, prompt.EntityRecord{
			ID:      view.ID.String(),
			Kind:    string(view.Kind),
			Label:   view.Label,
			Source:  view.Source,
			Trust:   string(view.Trust),
			Scope:   string(view.Scope),
			Preview: view.Preview,
			Digest:  view.Digest,
		})
	}
	return records
}

func (m *Model) entityToolsAvailable() bool {
	return m.entitiesEnabled() && m.toolsOn && m.toolRunner != nil
}

func (m *Model) entityContextTokenBudget() int {
	if m.cfg != nil && m.cfg.Entities.MaxContextTokens > 0 {
		return m.cfg.Entities.MaxContextTokens
	}
	return defaultEntityContextTokens
}

func (m *Model) registerResultEntities(results []tools.Result) []tools.Result {
	if !m.entitiesEnabled() {
		return results
	}
	for index := range results {
		if results[index].Err != nil || len(results[index].Entities) == 0 {
			continue
		}
		views := make([]entity.View, 0, len(results[index].Entities))
		for _, candidate := range results[index].Entities {
			if m.agentRunActive() {
				candidate.Scope = entity.ScopeAgentRun
				candidate.ScopeID = m.agentRunID()
			} else {
				candidate.Scope = entity.ScopeSession
				candidate.ScopeID = ""
			}
			view, err := m.entities.Put(candidate)
			if err != nil {
				continue
			}
			views = append(views, view)
		}
		if len(views) > 0 {
			results[index].Output = appendEntityReferences(results[index].Output, views)
		}
	}
	return results
}

func appendEntityReferences(output string, views []entity.View) string {
	var b strings.Builder
	b.WriteString(output)
	b.WriteString("\n\n[registered runtime entities — use the exact IDs for later detail requests]\n")
	for _, view := range views {
		fmt.Fprintf(&b, "- %s kind=%s label=%q source=%q\n", view.ID, view.Kind, view.Label, view.Source)
	}
	return strings.TrimRight(b.String(), "\n")
}

type entityDetailsWire struct {
	Entities     []entityResolutionWire `json:"entities"`
	TotalMatches *int                   `json:"total_matches,omitempty"`
	Truncated    bool                   `json:"truncated,omitempty"`
}

type entityResolutionWire struct {
	ID     string                  `json:"id"`
	Status entity.ResolutionStatus `json:"status"`
	Error  string                  `json:"error,omitempty"`
	Entity *entityViewWire         `json:"entity,omitempty"`
}

type entityViewWire struct {
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`
	Label     string             `json:"label"`
	Source    string             `json:"source"`
	Trust     string             `json:"trust"`
	Scope     string             `json:"scope"`
	Level     entity.Level       `json:"level"`
	Preview   string             `json:"preview,omitempty"`
	Payload   string             `json:"payload,omitempty"`
	Digest    string             `json:"digest"`
	Truncated bool               `json:"truncated,omitempty"`
	Redacted  bool               `json:"redacted,omitempty"`
	Metadata  entityMetadataWire `json:"metadata"`
}

type entityMetadataWire struct {
	Path        string `json:"path,omitempty"`
	URL         string `json:"url,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	SizeBytes   int    `json:"size_bytes,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
	Index       int    `json:"index,omitempty"`
	Count       int    `json:"count,omitempty"`
}

func (m *Model) handleEntityDetailsBatch(calls []tools.Call) (tea.Cmd, bool) {
	count := 0
	for _, call := range calls {
		if call.Tool == tools.ToolGetEntityDetails {
			count++
		}
	}
	if count == 0 {
		return nil, false
	}
	if count != len(calls) {
		return m.rejectWholeBatch(calls, fmt.Errorf("get_entity_details must be called alone; no calls in this batch were executed")), true
	}
	if !m.entitiesEnabled() {
		err := fmt.Errorf("get_entity_details is disabled")
		results := make([]tools.Result, len(calls))
		for i, call := range calls {
			results[i] = tools.Result{Call: call, Err: err, Meta: tools.ResultMeta{
				Outcome: tools.OutcomeFailed,
				Effect:  tools.EffectNone,
				Error:   &tools.ErrorInfo{Code: "unsupported_content", Retry: tools.RetryNone, Message: err.Error()},
			}}
		}
		m.advanceToolRound()
		m.toolErr += len(results)
		m.recordAgentToolResultsCount(results, false, uniformActionStatuses(len(results), agent.ActionBlocked))
		return m.sendToolResults(results), true
	}
	if m.toolDepth >= m.toolMaxIter() {
		m.overlayOpen = false
		m.keys.keysMode = false
		m.waitForApproval(newToolBatchPlan(calls), true)
		m.refreshViewport()
		return nil, true
	}
	results := make([]tools.Result, 0, len(calls))
	for _, call := range calls {
		result := tools.Result{Call: call}
		if call.InputErr != "" {
			result.Err = fmt.Errorf("invalid arguments for %s: %s", call.Tool, call.InputErr)
			result.Meta = tools.ResultMeta{
				Outcome: tools.OutcomeFailed, Effect: tools.EffectNone,
				Error: &tools.ErrorInfo{Code: "invalid_arguments", Retry: tools.RetryCorrectInput, Message: result.Err.Error()},
			}
		} else if err := tools.ValidateEntityDetailsCall(&call); err != nil {
			result.Err = err
			result.Meta = tools.ResultMeta{
				Outcome: tools.OutcomeFailed, Effect: tools.EffectNone,
				Error: &tools.ErrorInfo{Code: "invalid_arguments", Retry: tools.RetryCorrectInput, Message: err.Error()},
			}
		} else {
			result.Output, result.Meta = m.resolveEntityDetails(call)
		}
		results = append(results, result)
	}
	m.advanceToolRound()
	ok, failed := countToolOutcomes(results)
	m.toolOK += ok
	m.toolErr += failed
	m.recordAgentToolResultsCount(results, false, uniformActionStatuses(len(results), agent.ActionExecuted))
	return m.sendToolResults(results), true
}

// entityDetailsMeta implements the §23 "all-invalid -> failed, mixed ->
// partial, empty successful query -> OK" mapping: an empty resolutions list
// (a query that matched nothing) is a valid, complete empty result, never a
// fabricated failure; every resolution failing is OutcomeFailed; a mix of
// resolved and unresolved entities is OutcomePartial, since some but not all
// of the requested detail was actually delivered.
func entityDetailsMeta(resolutions []entity.Resolution) tools.ResultMeta {
	if len(resolutions) == 0 {
		return tools.ResultMeta{
			Outcome:  tools.OutcomeOK,
			Effect:   tools.EffectNone,
			Coverage: tools.Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true},
		}
	}
	ok, failed := 0, 0
	for _, resolution := range resolutions {
		if resolution.Status == entity.StatusOK {
			ok++
		} else {
			failed++
		}
	}
	meta := tools.ResultMeta{Effect: tools.EffectNone}
	switch {
	case failed == 0:
		meta.Outcome = tools.OutcomeOK
		meta.Coverage = tools.Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, RetainedBytes: int64(ok)}
	case ok == 0:
		meta.Outcome = tools.OutcomeFailed
		meta.Coverage = tools.Coverage{SourceComplete: false, CaptureComplete: false, PreviewComplete: true, Reasons: []string{"not_found"}}
		meta.Error = &tools.ErrorInfo{Code: "not_found", Retry: tools.RetryCorrectInput, Message: "no requested entity resolved"}
	default:
		meta.Outcome = tools.OutcomePartial
		meta.Coverage = tools.Coverage{SourceComplete: false, CaptureComplete: false, PreviewComplete: true, RetainedBytes: int64(ok), Reasons: []string{"not_found"}}
	}
	return meta
}

func (m *Model) resolveEntityDetails(call tools.Call) (string, tools.ResultMeta) {
	level := entity.Level(call.EntityLevel)
	ids := call.EntityIDs[:call.EntityIDCount]
	resolutions := m.entities.ResolveMany(ids, level, tools.MaxEntityDetailsIDs)
	wire := entityDetailsWire{Entities: make([]entityResolutionWire, 0, len(resolutions))}
	if call.SearchQuery != "" {
		kinds := make([]entity.Kind, 0, call.EntityKindCount)
		for index := 0; index < call.EntityKindCount; index++ {
			kinds = append(kinds, entity.Kind(call.EntityKinds[index]))
		}
		views, total := m.entities.SearchWithOptions(entity.SearchOptions{
			Query: call.SearchQuery,
			Kinds: kinds,
			Limit: tools.MaxEntityDetailsIDs,
		})
		wire.TotalMatches = &total
		wire.Truncated = total > len(views)
		for _, view := range views {
			resolutions = append(resolutions, entity.Resolution{ID: view.ID.String(), Status: entity.StatusOK, View: view})
		}
	}
	for _, resolution := range resolutions {
		item := entityResolutionWire{ID: resolution.ID, Status: resolution.Status, Error: resolution.Error}
		if resolution.Status == entity.StatusOK {
			view := resolution.View
			item.Entity = &entityViewWire{
				ID: view.ID.String(), Kind: string(view.Kind), Label: view.Label, Source: view.Source,
				Trust: string(view.Trust), Scope: string(view.Scope), Level: view.Level,
				Preview: view.Preview, Digest: view.Digest, Truncated: view.Truncated, Redacted: view.Redacted,
				Metadata: entityMetadataWire{
					Path: view.Metadata.Path, URL: view.Metadata.URL, ContentType: view.Metadata.ContentType,
					SizeBytes: view.Metadata.SizeBytes, StatusCode: view.Metadata.StatusCode,
					Index: view.Metadata.Index, Count: view.Metadata.Count,
				},
			}
			if view.Level == entity.LevelFull && view.Payload != "" {
				item.Entity.Payload = untrusted.Frame("entity_full", view.ID.String(), view.Payload)
			}
		}
		wire.Entities = append(wire.Entities, item)
	}
	meta := entityDetailsMeta(resolutions)
	encoded, err := json.Marshal(wire)
	if err != nil {
		return `{"entities":[],"error":"could not encode entity details"}`, tools.ResultMeta{
			Outcome: tools.OutcomeFailed, Effect: tools.EffectNone,
			Error: &tools.ErrorInfo{Code: "invalid_arguments", Retry: tools.RetryNone, Message: "could not encode entity details"},
		}
	}
	return string(encoded), meta
}
