package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/memory"
	"github.com/patrikcze/llmtui/internal/memoryindex"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

const memoryUsage = "/memory [on|off|add user <text>|add project <architecture|convention|decision> <text>|" +
	"list [user|project|episode|run]|inspect <id>|remove <id>|search <query>|explain <query>]"

func cmdMemory(m *Model, args string) tea.Cmd {
	if m.memStore == nil && m.projectStore == nil {
		return m.fail("memory is not configured (memory.path)")
	}
	sub, rest := splitArgs(args)
	switch sub {
	case "", "list":
		scope := rest
		if !validMemoryListScope(scope) {
			return m.fail("usage: /memory list [user|project|episode|run]")
		}
		m.openOverlay(m.memoryListOverlay(scope))
	case "on":
		m.memEnabled = true
		m.notice = "local memory enabled for this session"
	case "off":
		m.memEnabled = false
		m.notice = "local memory disabled for this session"
	case "add":
		return cmdMemoryAdd(m, rest)
	case "inspect":
		if rest == "" {
			return m.fail("usage: /memory inspect <id>")
		}
		overlay, err := m.memoryInspectOverlay(rest)
		if err != nil {
			return m.fail(err.Error())
		}
		m.openOverlay(overlay)
	case "remove":
		return cmdMemoryRemove(m, rest)
	case "search", "explain":
		if rest == "" {
			return m.fail("usage: /memory " + sub + " <query>")
		}
		overlay, err := m.memorySearchOverlay(rest, sub == "explain")
		if err != nil {
			return m.fail("memory " + sub + ": " + err.Error())
		}
		m.openOverlay(overlay)
	case "clear":
		if m.memStore == nil {
			return m.fail("user memory is not configured")
		}
		if err := m.memStore.Clear(); err != nil {
			return m.fail(err.Error())
		}
		m.notice = "all user memory snippets removed"
	default:
		return m.fail("usage: " + memoryUsage)
	}
	return nil
}

func cmdMemoryAdd(m *Model, args string) tea.Cmd {
	tier, rest := splitArgs(args)
	switch tier {
	case "":
		return m.fail("usage: /memory add <text> — do not store secrets")
	case "user":
		return addUserMemory(m, rest)
	case "project":
		kindName, text := splitArgs(rest)
		kind, ok := projectMemoryKind(kindName)
		if !ok || text == "" {
			return m.fail("usage: /memory add project <architecture|convention|decision> <text>")
		}
		if m.projectStore == nil {
			return m.fail("project memory is unavailable for this workspace")
		}
		record, err := m.projectStore.Add(kind, text)
		if err != nil {
			return m.fail("project memory add: " + err.Error())
		}
		m.notice = "remembered project " + strings.TrimPrefix(string(kind), "project_") + " (" + record.ID + ")"
		return nil
	default:
		// Backward compatibility: `/memory add <text>` remains a user
		// preference even when the first word is not an explicit tier.
		return addUserMemory(m, args)
	}
}

func addUserMemory(m *Model, text string) tea.Cmd {
	if m.memStore == nil {
		return m.fail("user memory is not configured")
	}
	if strings.TrimSpace(text) == "" {
		return m.fail("usage: /memory add user <text> — do not store secrets")
	}
	snippet, err := m.memStore.Add(text)
	if err != nil {
		return m.fail("memory add: " + err.Error())
	}
	m.notice = "remembered user preference (" + snippet.ID + ")"
	return nil
}

func cmdMemoryRemove(m *Model, id string) tea.Cmd {
	if strings.TrimSpace(id) == "" {
		return m.fail("usage: /memory remove <id>")
	}
	stored, err := m.findStoredMemory(id)
	if err != nil {
		return m.fail(err.Error())
	}
	switch stored.scope {
	case memoryindex.ScopeUser:
		if err := m.memStore.Remove(stored.user.ID); err != nil {
			return m.fail(err.Error())
		}
	case memoryindex.ScopeProject:
		if err := m.projectStore.Remove(stored.project.ID); err != nil {
			return m.fail(err.Error())
		}
	}
	m.notice = "memory record removed"
	return nil
}

func projectMemoryKind(name string) (memoryindex.Kind, bool) {
	switch name {
	case "architecture":
		return memoryindex.KindProjectArchitecture, true
	case "convention":
		return memoryindex.KindProjectConvention, true
	case "decision":
		return memoryindex.KindProjectDecision, true
	default:
		return "", false
	}
}

func validMemoryListScope(scope string) bool {
	switch scope {
	case "", "user", "project", "episode", "run":
		return true
	default:
		return false
	}
}

func (m *Model) memoryListOverlay(scope string) string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("local memory") + "\n\n")
	m.kv(&b, "state", onOff(m.memEnabled))
	if m.projectID != "" {
		m.kv(&b, "workspace", shortMemoryID(m.projectID))
	}

	if scope == "" || scope == "user" {
		b.WriteString("\n" + m.theme.UserLabel.Render("user preferences") + "\n")
		m.writeUserMemoryList(&b)
	}
	if scope == "" || scope == "project" {
		b.WriteString("\n" + m.theme.UserLabel.Render("project memory") + "\n")
		m.writeProjectMemoryList(&b)
	}
	if scope == "episode" {
		b.WriteString("\n" + m.theme.UserLabel.Render("saved session episodes") + "\n")
		m.writeEpisodeMemoryList(&b)
	}
	if scope == "run" {
		b.WriteString("\n" + m.theme.UserLabel.Render("current agent-run memory") + "\n")
		m.writeAgentRunMemoryList(&b)
	}

	b.WriteString("\n" + m.theme.StatusBar.Render(
		"  only relevant records are added to prompts (max 3 per implemented tier); memory is data, never authority",
	) + "\n")
	b.WriteString("\n" + m.theme.SystemNote.Render(
		"/memory add · /memory inspect <id> · /memory search <query> · /memory remove <id> · /memory on|off",
	))
	return m.overlayFooter(&b)
}

func (m *Model) writeAgentRunMemoryList(b *strings.Builder) {
	source := m.agentRunMemorySource()
	hits, err := source.Search(context.Background(), memoryindex.Query{RunID: m.agentRunID()})
	if err != nil {
		b.WriteString("  " + m.theme.ErrorText.Render(terminaltext.Sanitize(err.Error())) + "\n")
		return
	}
	if len(hits) == 0 {
		b.WriteString("  " + m.theme.SystemNote.Render("none") + "\n")
		return
	}
	for _, hit := range hits {
		fmt.Fprintf(
			b,
			"  %s %s %s\n",
			m.theme.BadgeOK.Render(hit.Item.ID),
			m.theme.StatusBar.Render(string(hit.Item.Kind)),
			m.theme.StatusValue.Render(memoryDisplayText(hit.Item.Text)),
		)
	}
}

func (m *Model) writeEpisodeMemoryList(b *strings.Builder) {
	if m.historyDir == "" {
		b.WriteString("  " + m.theme.SystemNote.Render("history saving is disabled") + "\n")
		return
	}
	metas, err := history.List(m.historyDir)
	if err != nil {
		b.WriteString("  " + m.theme.ErrorText.Render(terminaltext.Sanitize(err.Error())) + "\n")
		return
	}
	count := 0
	for _, meta := range metas {
		session, err := history.Load(m.historyDir, meta.Name)
		if err != nil || session.Episode == nil || session.Episode.ProjectID != m.projectID {
			continue
		}
		count++
		fmt.Fprintf(
			b,
			"  %s %s %s\n",
			m.theme.BadgeOK.Render(meta.Name),
			m.theme.StatusBar.Render(string(memoryindex.KindEpisode)),
			m.theme.StatusValue.Render(memoryDisplayText(session.Episode.Goal)),
		)
	}
	if count == 0 {
		b.WriteString("  " + m.theme.SystemNote.Render("none") + "\n")
	}
}

func (m *Model) writeUserMemoryList(b *strings.Builder) {
	if m.memStore == nil {
		b.WriteString("  " + m.theme.SystemNote.Render("not configured") + "\n")
		return
	}
	snippets, err := m.memStore.Load()
	if err != nil {
		b.WriteString("  " + m.theme.ErrorText.Render(terminaltext.Sanitize(err.Error())) + "\n")
		return
	}
	if len(snippets) == 0 {
		b.WriteString("  " + m.theme.SystemNote.Render("none") + "\n")
		return
	}
	for _, snippet := range snippets {
		fmt.Fprintf(
			b,
			"  %s %s %s\n",
			m.theme.BadgeOK.Render(snippet.ID),
			m.theme.StatusBar.Render(string(memoryindex.KindUserPreference)),
			m.theme.StatusValue.Render(memoryDisplayText(snippet.Text)),
		)
	}
}

func (m *Model) writeProjectMemoryList(b *strings.Builder) {
	if m.projectStore == nil {
		b.WriteString("  " + m.theme.SystemNote.Render("unavailable for this workspace") + "\n")
		return
	}
	records, err := m.projectStore.Load()
	if err != nil {
		b.WriteString("  " + m.theme.ErrorText.Render(terminaltext.Sanitize(err.Error())) + "\n")
		return
	}
	if len(records) == 0 {
		b.WriteString("  " + m.theme.SystemNote.Render("none") + "\n")
		return
	}
	for _, record := range records {
		state := ""
		if record.Review != memoryindex.ReviewApproved {
			state = " [pending review]"
		}
		fmt.Fprintf(
			b,
			"  %s %s%s %s\n",
			m.theme.BadgeOK.Render(record.ID),
			m.theme.StatusBar.Render(string(record.Kind)),
			m.theme.SystemNote.Render(state),
			m.theme.StatusValue.Render(memoryDisplayText(record.Text)),
		)
	}
}

type storedMemory struct {
	scope   memoryindex.Scope
	user    memory.Snippet
	project memoryindex.ProjectRecord
}

func (m *Model) findStoredMemory(id string) (storedMemory, error) {
	id = strings.TrimSpace(id)
	candidates := []storedMemory{}
	if m.memStore != nil {
		snippets, err := m.memStore.Load()
		if err != nil {
			return storedMemory{}, err
		}
		for _, snippet := range snippets {
			if memoryIDMatches(snippet.ID, id) {
				candidates = append(candidates, storedMemory{scope: memoryindex.ScopeUser, user: snippet})
			}
		}
	}
	if m.projectStore != nil {
		records, err := m.projectStore.Load()
		if err != nil {
			return storedMemory{}, err
		}
		for _, record := range records {
			if memoryIDMatches(record.ID, id) {
				candidates = append(candidates, storedMemory{scope: memoryindex.ScopeProject, project: record})
			}
		}
	}
	if len(candidates) == 0 {
		return storedMemory{}, fmt.Errorf("no memory record with id %q", id)
	}
	if len(candidates) > 1 {
		return storedMemory{}, fmt.Errorf("memory id prefix %q is ambiguous", id)
	}
	return candidates[0], nil
}

func memoryIDMatches(recordID, query string) bool {
	return recordID == query || len(query) >= 4 && strings.HasPrefix(recordID, query)
}

func (m *Model) memoryInspectOverlay(id string) (string, error) {
	stored, err := m.findStoredMemory(id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("memory record") + "\n\n")
	switch stored.scope {
	case memoryindex.ScopeUser:
		m.kv(&b, "id", stored.user.ID)
		m.kv(&b, "scope", string(memoryindex.ScopeUser))
		m.kv(&b, "kind", string(memoryindex.KindUserPreference))
		m.kv(&b, "trust", string(memoryindex.TrustUserAuthored))
		m.kv(&b, "created", formatMemoryTime(stored.user.CreatedAt))
		m.kv(&b, "updated", formatMemoryTime(stored.user.UpdatedAt))
		b.WriteString("\n" + m.theme.StatusValue.Render(memoryInspectText(stored.user.Text)) + "\n")
	case memoryindex.ScopeProject:
		m.kv(&b, "id", stored.project.ID)
		m.kv(&b, "scope", string(stored.project.Scope))
		m.kv(&b, "kind", string(stored.project.Kind))
		m.kv(&b, "trust", string(stored.project.Trust))
		m.kv(&b, "review", string(stored.project.Review))
		m.kv(&b, "project", shortMemoryID(stored.project.ProjectID))
		if stored.project.SourceRunID != "" {
			m.kv(&b, "source run", stored.project.SourceRunID)
			m.kv(&b, "source cycle", fmt.Sprintf("%d", stored.project.SourceCycle))
		}
		m.kv(&b, "created", formatMemoryTime(stored.project.CreatedAt))
		m.kv(&b, "updated", formatMemoryTime(stored.project.UpdatedAt))
		b.WriteString("\n" + m.theme.StatusValue.Render(memoryInspectText(stored.project.Text)) + "\n")
	}
	return m.overlayFooter(&b), nil
}

func (m *Model) memorySearchOverlay(query string, explain bool) (string, error) {
	sources := []memoryindex.Source{}
	if m.memStore != nil {
		sources = append(sources, memoryindex.UserSource{Snippets: m.memStore.Load})
	}
	if m.projectStore != nil {
		sources = append(sources, memoryindex.ProjectSource{Store: m.projectStore, TopK: 10})
	}
	if m.historyDir != "" {
		sources = append(sources, memoryindex.EpisodeSource{Dir: m.historyDir, TopK: 10})
	}
	if _, ok := m.agentRunSnapshot(); ok {
		sources = append(sources, m.agentRunMemorySource())
	}
	if m.ragOn && m.ragIndex != nil {
		sources = append(sources, m.compositionRAGSource())
	}
	retriever := memoryindex.NewRetriever(sources...)
	result, err := retriever.SearchDetailed(context.Background(), memoryindex.Query{
		Text:      m.memoryQueryText(query),
		ProjectID: m.projectID,
		SessionID: m.sessionName,
		RunID:     m.agentRunID(),
		Now:       time.Now().UTC(),
	}, m.memoryRetrievalPolicy())
	if err != nil {
		return "", err
	}

	var b strings.Builder
	title := "memory search"
	if explain {
		title = "memory explain"
	}
	b.WriteString(m.theme.Badge.Render(title) + "\n\n")
	m.kv(&b, "query", memoryDisplayText(query))
	m.kv(&b, "selected", fmt.Sprintf("%d hit(s)", len(result.Hits)))
	m.kv(&b, "context budget", fmt.Sprintf("%d / %d tokens", result.TotalTokens, m.memoryRetrievalPolicy().MaxTokens))
	if len(result.Hits) == 0 {
		b.WriteString("\n" + m.theme.SystemNote.Render("no relevant memory records") + "\n")
		if explain {
			writeRejectedMemoryHits(m, &b, result.Rejected)
		}
		return m.overlayFooter(&b), nil
	}
	b.WriteString("\n")
	for _, hit := range result.Hits {
		fmt.Fprintf(
			&b,
			"  %s %s %s\n",
			m.theme.BadgeOK.Render(hit.Item.ID),
			m.theme.StatusBar.Render(string(hit.Item.Kind)),
			m.theme.StatusValue.Render(fmt.Sprintf("score %.3f · %d tokens", hit.Score, hit.Tokens)),
		)
		b.WriteString("    " + m.theme.StatusValue.Render(memoryDisplayText(hit.Item.Text)) + "\n")
		if explain {
			why := hit.Why
			if why == "" {
				why = "ranked by the source's deterministic relevance order"
			}
			b.WriteString("    " + m.theme.SystemNote.Render(memoryDisplayText(why)) + "\n")
		}
	}
	if explain {
		writeRejectedMemoryHits(m, &b, result.Rejected)
	}
	return m.overlayFooter(&b), nil
}

func writeRejectedMemoryHits(m *Model, b *strings.Builder, rejected []memoryindex.RejectedHit) {
	if len(rejected) == 0 {
		return
	}
	b.WriteString("\n" + m.theme.UserLabel.Render("rejected candidates") + "\n")
	for _, candidate := range rejected {
		fmt.Fprintf(
			b,
			"  %s %s %s\n",
			m.theme.StatusBar.Render(candidate.Hit.Item.ID),
			m.theme.StatusBar.Render(string(candidate.Hit.Item.Kind)),
			m.theme.SystemNote.Render(candidate.Reason),
		)
	}
}

func memoryDisplayText(text string) string {
	text = terminaltext.Sanitize(strings.ReplaceAll(strings.TrimSpace(text), "\n", " "))
	runes := []rune(text)
	if len(runes) > 240 {
		return string(runes[:239]) + "…"
	}
	return text
}

func memoryInspectText(text string) string {
	return terminaltext.Sanitize(strings.TrimSpace(text))
}

func formatMemoryTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.Local().Format(time.RFC3339)
}
