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
}

func (m *Model) entityPromptRecords() []prompt.EntityRecord {
	if !m.entitiesEnabled() {
		return nil
	}
	maxTokens := m.cfg.Entities.MaxContextTokens
	if maxTokens <= 0 {
		maxTokens = defaultEntityContextTokens
	}
	maxBytes := maxTokens
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
	Entities []entityResolutionWire `json:"entities"`
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
		results := make([]tools.Result, len(calls))
		for i, call := range calls {
			results[i] = tools.Result{Call: call, Err: fmt.Errorf("get_entity_details is disabled")}
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
		} else if err := tools.ValidateEntityDetailsCall(&call); err != nil {
			result.Err = err
		} else {
			result.Output = m.resolveEntityDetails(call)
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

func (m *Model) resolveEntityDetails(call tools.Call) string {
	level := entity.Level(call.EntityLevel)
	ids := call.EntityIDs[:call.EntityIDCount]
	resolutions := m.entities.ResolveMany(ids, level, tools.MaxEntityDetailsIDs)
	wire := entityDetailsWire{Entities: make([]entityResolutionWire, 0, len(resolutions))}
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
	encoded, err := json.Marshal(wire)
	if err != nil {
		return `{"entities":[],"error":"could not encode entity details"}`
	}
	return string(encoded)
}
