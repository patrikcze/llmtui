package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/untrusted"
)

const defaultEntityContextTokens = 1200

type observedFileVersion struct {
	ResourceID string
	Version    entity.FileVersion
	ObservedAt time.Time
}

// admitWebRefresh makes freshness epochs controller-owned. A model may ask
// for refresh, but cannot churn an arbitrary token to bypass the progress
// ledger; each admitted refresh gets one monotonic session identity.
func (m *Model) admitWebRefresh(calls []tools.Call) []tools.Call {
	out := append([]tools.Call(nil), calls...)
	for i := range out {
		if out[i].Tool != tools.ToolWebFetch || strings.ToLower(strings.TrimSpace(out[i].WebCacheMode)) != "refresh" {
			out[i].WebRefreshEpoch = ""
			continue
		}
		m.webRefreshEpoch++
		out[i].WebRefreshEpoch = fmt.Sprintf("refresh-%d", m.webRefreshEpoch)
	}
	return out
}

func fileVersionKey(path string) string {
	return filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
}

// bindObservedEditVersions turns a model's edit or overwrite request into a controller
// precondition using only versions already delivered in this conversation.
// An unknown or stale selector remains unbound and is rejected by execution;
// it is never guessed from the current filesystem.
func (m *Model) bindObservedEditVersions(calls []tools.Call) []tools.Call {
	if len(calls) == 0 || len(m.observedFileVersions) == 0 {
		return calls
	}
	out := append([]tools.Call(nil), calls...)
	for i := range out {
		if (out[i].Tool != tools.ToolEditFile && out[i].Tool != tools.ToolWriteFile) || out[i].ExpectedVersion != nil {
			continue
		}
		var observed observedFileVersion
		var ok bool
		if id := strings.TrimSpace(out[i].ExpectedResourceID); id != "" {
			observed, ok = m.observedFileVersions["id:"+id]
		} else {
			observed, ok = m.observedFileVersions["path:"+fileVersionKey(out[i].Path)]
		}
		if !ok {
			continue
		}
		if fileVersionKey(observed.Version.Path) != fileVersionKey(out[i].Path) {
			out[i].InputErr = fmt.Sprintf("expected_resource_id %q belongs to %q, not %q", out[i].ExpectedResourceID, observed.Version.Path, out[i].Path)
			continue
		}
		version := observed.Version
		out[i].ExpectedVersion = &version
		if out[i].ExpectedResourceID == "" {
			out[i].ExpectedResourceID = observed.ResourceID
		}
	}
	return out
}

// recordDeliveredFileVersions records metadata only after the result message
// has been appended to the conversation. This matches the model-observation
// boundary: a captured version that was never delivered cannot authorize an
// edit.
func (m *Model) recordDeliveredFileVersions(results []tools.Result) {
	if len(results) == 0 {
		return
	}
	if m.observedFileVersions == nil {
		m.observedFileVersions = make(map[string]observedFileVersion)
	}
	for _, result := range results {
		version := result.Meta.FileVersion
		if result.Err != nil || version == nil || !version.Complete || version.Path == "" {
			continue
		}
		observed := observedFileVersion{Version: *version, ObservedAt: time.Now().UTC()}
		observed.ResourceID = strings.TrimSpace(result.ResourceID)
		m.observedFileVersions["path:"+fileVersionKey(version.Path)] = observed
		if observed.ResourceID != "" {
			m.observedFileVersions["id:"+observed.ResourceID] = observed
		}
	}
}

func (m *Model) pinPendingVersions(plan toolBatchPlan) {
	m.releasePendingVersionPins()
	if m.entities == nil {
		return
	}
	for _, call := range plan.runnableCalls() {
		id := strings.TrimSpace(call.ExpectedResourceID)
		if id == "" {
			continue
		}
		parsed, err := entity.ParseID(id)
		if err != nil || !m.entities.RetainBody(parsed) {
			continue
		}
		m.pendingVersionPins = append(m.pendingVersionPins, parsed)
	}
}

func (m *Model) releasePendingVersionPins() {
	for _, id := range m.pendingVersionPins {
		if m.entities != nil {
			m.entities.ReleaseBody(id)
		}
	}
	m.pendingVersionPins = nil
}

func (m *Model) entitiesEnabled() bool {
	return m.cfg != nil && m.cfg.Entities.Enabled && m.entities != nil
}

// outputStorageEnabled reports whether captured tool-result bodies (Phase
// 2b) should be published for later resource_id read-back. An empty value
// and any value other than the literal "off" behave like the default
// "memory" — see EntitiesConfig.OutputStorage's doc comment for why this
// package does not fail config load over an unrecognized value.
func (m *Model) outputStorageEnabled() bool {
	return m.cfg == nil || m.cfg.Entities.OutputStorage != "off"
}

func (m *Model) resetEntities() {
	m.webRefreshEpoch = 0
	if m.toolRunner != nil {
		m.toolRunner.ResetSearchCursors()
	}
	if m.entities != nil {
		m.entities.Reset()
	}
	if m.webSnapshots != nil {
		m.webSnapshots.Reset()
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
	captureStorageOn := m.outputStorageEnabled()
	for index := range results {
		if results[index].Err != nil {
			continue
		}
		if len(results[index].Entities) > 0 {
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
				for _, view := range views {
					results[index].References = appendMessageReferences(results[index].References, provider.MessageReference{
						ID: view.ID.String(), Kind: string(view.Kind), Label: view.Label,
					})
				}
			}
		}
		if results[index].Call.ResourceID != "" {
			results[index].References = appendMessageReferences(results[index].References, provider.MessageReference{
				ID: results[index].Call.ResourceID, Kind: string(entity.KindFile), Label: "retained body",
			})
		}
		// Captures (Phase 2b-ii): a producer-retained body beyond what
		// Output already shows (currently only a capped run_command
		// result). This is a quiet, deliberate no-op when output storage is
		// off or nothing was captured — never an error surfaced to the
		// model, since the underlying tool call already succeeded and
		// Output already carries its capped preview either way.
		if captureStorageOn && len(results[index].Captures) > 0 {
			resourceViews := m.publishResultCaptures(results[index].Call, results[index].Captures)
			if len(resourceViews) > 0 {
				results[index].Output = appendResourceReferences(results[index].Output, resourceViews)
				for _, view := range resourceViews {
					if m.webSnapshots != nil {
						if indexer, ok := any(m.webSnapshots).(tools.WebSnapshotIndexer); ok && view.Kind == entity.KindWebPage {
							indexer.IndexWebSnapshot(view.Resource.RequestedURL, view.ID, view.Resource)
						}
					}
					if results[index].ResourceID == "" && (view.Resource.FileVersion != nil || view.Kind == entity.KindSearchResult || view.Kind == entity.KindToolOutput) {
						results[index].ResourceID = view.ID.String()
					}
					results[index].References = appendMessageReferences(results[index].References, provider.MessageReference{
						ID: view.ID.String(), Kind: string(view.Kind), Label: view.Label,
					})
				}
			}
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

// publishResultCaptures publishes each of a result's unpublished Captures
// (internal/tools' bounded, producer-retained bodies) into the entity
// registry as resource bodies, returning a view for every one that
// succeeded. A Publish failure (e.g. entity.ErrBodyCapacityExhausted, every
// slot pinned) is silently skipped, not surfaced as a tool error — the
// command already ran and its capped Output is unaffected; retention merely
// could not happen this time. context.Background() is used deliberately:
// neither registerResultEntities nor any of sendToolResults' call sites
// carries a cancellable context down to this point (the batch's own
// execution context is only used while the batch runs, not once its
// results are being finalized for the model), and Publish against the
// in-memory backend is fast enough that this is not a blocking concern for
// Bubble Tea's Update (see CLAUDE.md "Update must never block").
func (m *Model) publishResultCaptures(call tools.Call, captures []tools.Capture) []entity.ResourceView {
	views := make([]entity.ResourceView, 0, len(captures))
	for _, capture := range captures {
		candidate := entity.Candidate{
			Kind:  capture.Kind,
			Label: capture.Label,
			Trust: capture.Trust,
			Provenance: entity.Provenance{
				Source:    "tools",
				Operation: call.Tool,
				CallID:    call.ID,
			},
			Resource: entity.ResourceMetadata{
				ContentType: capture.ContentType,
				BodyDigest:  capture.BodyDigest,
			},
		}
		candidate.Resource = capture.Resource
		if candidate.Resource.ContentType == "" {
			candidate.Resource.ContentType = capture.ContentType
		}
		if candidate.Resource.BodyDigest == "" {
			candidate.Resource.BodyDigest = capture.BodyDigest
		}
		if m.agentRunActive() {
			candidate.Scope = entity.ScopeAgentRun
			candidate.ScopeID = m.agentRunID()
		} else {
			candidate.Scope = entity.ScopeSession
			candidate.ScopeID = ""
		}
		view, err := m.entities.Publish(context.Background(), candidate, capture.Body)
		if err != nil {
			continue
		}
		views = append(views, view)
	}
	return views
}

// appendResourceReferences mirrors appendEntityReferences for published
// resource bodies. Kept short — this text is model-visible and counts
// against context budget — it never repeats the retained body itself, only
// the ID to read it back with.
func appendResourceReferences(output string, views []entity.ResourceView) string {
	var b strings.Builder
	b.WriteString(output)
	b.WriteString("\n\n[retained output — read it back with read_file resource_id instead of rerunning this command]\n")
	for _, view := range views {
		fmt.Fprintf(&b, "- %s kind=%s bytes=%d\n", view.ID, view.Kind, view.SizeBytes)
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
