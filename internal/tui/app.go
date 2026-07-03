// Package tui implements the full-screen Bubble Tea chat interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/cache"
	"github.com/patrikcze/llmtui/internal/chat"
	"github.com/patrikcze/llmtui/internal/clipboard"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/contextmgr"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/memory"
	"github.com/patrikcze/llmtui/internal/modelprofile"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/tui/components"
	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// Options configures the chat UI.
type Options struct {
	Config     *config.Config
	Provider   provider.Provider
	Model      string
	ConfigPath string // path of the loaded config file, for /config
}

// errStreamIdle is the cancellation cause when the inactivity watchdog fires:
// the server sent no token for network.timeout, so we treat the stream as
// stalled. Distinct from a user Esc (context.Canceled) so we can report why.
var errStreamIdle = errors.New("stream idle timeout")

type healthMsg struct {
	err      error
	provider string // which provider was checked, to discard stale results
	initial  bool   // startup check: only then may we fall back to demo mode
}

type streamEventMsg struct {
	event provider.ChatEvent
	ok    bool
}

type clipboardImageMsg struct {
	img provider.Image
	err error
}

type copyResultMsg struct {
	chars int
	err   error
}

// Model is the root Bubble Tea model for the chat screen.
type Model struct {
	cfg      *config.Config
	theme    styles.Theme
	prov     provider.Provider
	model    string
	session  *chat.Session
	renderer *glamour.TermRenderer

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model

	width, height int
	ready         bool
	connected     bool
	demoMode      bool
	thinking      bool
	streamBuf     strings.Builder
	stream        <-chan provider.ChatEvent
	streamStart   time.Time
	lastTPS       float64
	errText       string
	cancelStream  context.CancelFunc
	streamCtx     context.Context
	idleWatchdog  *time.Timer
	idleTimeout   time.Duration
	reasoningLen  int // chars of "thinking" streamed before the visible answer
	attachments   []provider.Image
	frame         int
	renderWidth   int
	mouseEnabled  bool
	notice        string
	overlayOpen   bool
	sugs          []slashCommand
	sugIdx        int
	historyDir    string
	sessionName   string
	inputLines    int
	ctrlCAt       time.Time

	// Exit summary bookkeeping, reported after the TUI closes.
	startedAt  time.Time
	apiTime    time.Duration
	modelStats []modelUsageStat
	sentCount  int
	replyCount int
	savedPath  string

	// Workspace file tools (list/read/write under the launch directory).
	toolsOn     bool
	toolRunner  *tools.Runner
	toolDepth   int  // auto follow-up rounds for the current user turn
	bypassCache bool // skip the response cache for the next dispatch

	// Local-LLM experience helpers.
	responseCache *cache.Cache
	memStore      *memory.Store
	memEnabled    bool
	promptMode    string // "" = follow template/config
	profileMode   string // "auto" or a profile name
	profiles      []modelprofile.Profile
	template      string
	summary       string
	ctxStrategy   string
	ctxUsed       int
	ctxWindow     int
	lastUserMsg   string
	lastImages    []provider.Image
	lastDebug     debugInfo
	debugMode     bool
	keysMode      bool
	keysRaw       bool
	keyLog        []string
	cfgPath       string
}

// New builds the chat model.
func New(opts Options) *Model {
	t := styles.ByName(opts.Config.UI.Theme)

	ta := textarea.New()
	ta.Placeholder = "Ask anything… (/ for commands, Enter to send)"
	ta.Prompt = "┃ "
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = t.Spinner

	cfg := opts.Config

	profileMode := "auto"
	if cfg.Chat.ModelProfile != "" {
		profileMode = cfg.Chat.ModelProfile
	}

	ctxStrategy := cfg.Context.Strategy
	if !contextmgr.ValidStrategy(ctxStrategy) {
		ctxStrategy = contextmgr.StrategyAuto
	}

	m := &Model{
		cfg:          opts.Config,
		theme:        t,
		prov:         opts.Provider,
		model:        opts.Model,
		session:      chat.NewSession(opts.Config.Chat.SystemPrompt),
		input:        ta,
		spinner:      sp,
		mouseEnabled: true,
		sessionName:  history.NewSessionName(time.Now()),
		inputLines:   1,
		startedAt:    time.Now(),

		memEnabled:  cfg.Memory.Enabled,
		profileMode: profileMode,
		ctxStrategy: ctxStrategy,
		cfgPath:     opts.ConfigPath,
		toolsOn:     cfg.Tools.Enabled,
	}
	m.rebuildFromConfig()
	return m
}

// rebuildFromConfig (re)derives the components that mirror the config:
// history dir, response cache, memory store, and model profiles. It runs at
// startup and after /config reload; session-scoped choices the user made at
// runtime (/profile, /context strategy, /memory on|off) are left alone.
func (m *Model) rebuildFromConfig() {
	cfg := m.cfg

	m.historyDir = ""
	if cfg.Chat.SaveHistory && cfg.Chat.HistoryDir != "" {
		if dir, err := history.ExpandHome(cfg.Chat.HistoryDir); err == nil {
			m.historyDir = dir
		}
	}

	m.responseCache = nil
	if dir, err := history.ExpandHome(cfg.Cache.Path); err == nil && dir != "" {
		ttl, _ := time.ParseDuration(cfg.Cache.TTL)
		m.responseCache = cache.New(dir, ttl, cfg.Cache.MaxSizeMB, cfg.Cache.Enabled)
	}

	m.memStore = nil
	if path, err := history.ExpandHome(cfg.Memory.Path); err == nil && path != "" {
		m.memStore = memory.NewStore(path, cfg.Memory.MaxSnippets)
	}

	m.toolRunner = nil
	if wd, err := os.Getwd(); err == nil {
		m.toolRunner = tools.NewRunner(wd, cfg.Tools.MaxFileKB)
	}

	// Config-defined profiles are matched before built-ins.
	profiles := make([]modelprofile.Profile, 0, len(cfg.ModelProfiles)+4)
	for name, pc := range cfg.ModelProfiles {
		profiles = append(profiles, modelprofile.Profile{
			Name:                 name,
			Match:                pc.Match,
			ContextWindow:        pc.ContextWindow,
			PreferredTemperature: pc.PreferredTemperature,
			SupportsJSONMode:     pc.SupportsJSONMode,
			PromptStyle:          pc.PromptStyle,
			ReasoningHint:        pc.ReasoningHint,
		})
	}
	m.profiles = append(profiles, modelprofile.BuiltIn()...)
}

// sessionRecord builds the persistable form of the current session.
func (m *Model) sessionRecord() history.Session {
	prof, _ := m.activeProfile()
	return history.Session{
		Provider:   m.prov.Name(),
		Model:      m.model,
		Template:   m.template,
		PromptMode: m.effectivePromptMode(),
		Profile:    prof.Name,
		Messages:   m.session.Messages,
		Prompt:     m.session.TotalPromptTokens,
		Reply:      m.session.TotalCompletionTokens,
		Estimated:  m.session.AnyEstimated,
	}
}

// Init starts the spinner and kicks off the provider health check.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, textarea.Blink, m.checkHealth(true))
}

func (m *Model) checkHealth(initial bool) tea.Cmd {
	prov := m.prov
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return healthMsg{err: prov.HealthCheck(ctx), provider: prov.Name(), initial: initial}
	}
}

func waitForEvent(stream <-chan provider.ChatEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-stream
		return streamEventMsg{event: ev, ok: ok}
	}
}

// Update handles all Bubble Tea messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// The /keys inspector sees every input event before normal handling.
	if m.keysMode {
		switch msg.(type) {
		case tea.KeyMsg:
			return m.updateKeysMode(msg)
		default:
			if _, ok := extendedKeySeq(msg); ok {
				return m.updateKeysMode(msg)
			}
		}
	}

	// Modified Enter (Shift+Enter etc.) arrives as a raw CSI sequence when
	// the terminal supports modifyOtherKeys; treat it as a newline.
	if seq, ok := extendedKeySeq(msg); ok {
		if isModifiedEnter(seq) && !m.overlayOpen {
			m.input.InsertString("\n")
			m.syncInputHeight()
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		if m.overlayOpen {
			return m.updateOverlay(msg)
		}
		if len(m.sugs) > 0 {
			switch msg.Type {
			case tea.KeyUp:
				m.sugIdx = (m.sugIdx - 1 + len(m.sugs)) % len(m.sugs)
				return m, nil
			case tea.KeyDown:
				m.sugIdx = (m.sugIdx + 1) % len(m.sugs)
				return m, nil
			case tea.KeyTab:
				m.input.SetValue("/" + m.sugs[m.sugIdx].name + " ")
				m.input.CursorEnd()
				m.updateSuggestions()
				return m, nil
			}
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			return m.handleCtrlC()
		case tea.KeyCtrlS:
			m.saveWithNotice()
			return m, nil
		case tea.KeyCtrlJ:
			// Insert a newline; the input box grows with the content.
			m.input.InsertString("\n")
			m.syncInputHeight()
			return m, nil
		case tea.KeyCtrlL:
			m.session.Clear()
			m.refreshViewport()
			return m, nil
		case tea.KeyCtrlV:
			return m, m.pasteImage()
		case tea.KeyCtrlX:
			if len(m.attachments) > 0 {
				m.attachments = m.attachments[:len(m.attachments)-1]
				m.relayout()
			}
			return m, nil
		case tea.KeyCtrlY:
			return m, m.copyLastReply()
		case tea.KeyCtrlO:
			// Release the mouse so the terminal's native selection works;
			// press again to get wheel scrolling back.
			m.mouseEnabled = !m.mouseEnabled
			if m.mouseEnabled {
				m.notice = "mouse scrolling on — text selection captured by app"
				return m, tea.EnableMouseCellMotion
			}
			m.notice = "text selection on — select & copy with your terminal, ctrl+o to switch back"
			return m, tea.DisableMouse
		case tea.KeyEsc:
			if m.thinking && m.cancelStream != nil {
				// Stop generation, keeping the partial reply.
				m.cancelStream()
				m.finishStream(nil)
				m.errText = "generation stopped"
				m.refreshViewport()
			} else if strings.HasPrefix(m.input.Value(), "/") {
				m.input.Reset()
				m.updateSuggestions()
				m.syncInputHeight()
			}
			return m, nil
		case tea.KeyEnter:
			// Alt/Option+Enter inserts a newline (with "Option as Meta"
			// enabled on macOS terminals; see README).
			if msg.Alt {
				m.input.InsertString("\n")
				m.syncInputHeight()
				return m, nil
			}
			// Universal fallback: a trailing backslash continues the line.
			if val := m.input.Value(); strings.HasSuffix(val, "\\") {
				m.input.SetValue(strings.TrimSuffix(val, "\\"))
				m.input.CursorEnd()
				m.input.InsertString("\n")
				m.syncInputHeight()
				return m, nil
			}
			if strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
				return m, m.runSlashCommand()
			}
			if !m.thinking {
				return m, m.send()
			}
			return m, nil
		}

	case healthMsg:
		if msg.provider != m.prov.Name() {
			// Stale result from a provider we already switched away from.
			return m, nil
		}
		switch {
		case msg.err == nil:
			m.connected = true
			m.demoMode = false
		case msg.initial:
			// Backend unreachable at startup: fall back to the demo provider.
			m.connected = false
			m.demoMode = true
			m.prov = mock.New()
			m.model = "demo-model"
		default:
			// A mid-session check (e.g. after /provider switch) must never
			// silently replace the user's chosen provider and model.
			m.connected = false
			m.errText = fmt.Sprintf("%s health check failed: %v", msg.provider, msg.err)
		}
		m.refreshViewport()
		return m, nil

	case firstStreamMsg:
		m.stream = msg.stream
		m.lastDebug.Retries = msg.retries
		return m.handleStreamEvent(streamEventMsg{event: msg.event, ok: msg.ok})

	case streamEventMsg:
		return m.handleStreamEvent(msg)

	case clipboardImageMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()
		} else {
			m.attachments = append(m.attachments, msg.img)
			m.errText = ""
		}
		m.relayout()
		m.refreshViewport()
		return m, nil

	case doctorResultMsg:
		m.notice = ""
		m.openOverlay(m.doctorOverlay(msg.report))
		return m, nil

	case modelsResultMsg:
		if msg.err != nil {
			m.errText = "list models: " + msg.err.Error()
			m.refreshViewport()
		} else {
			m.openOverlay(m.modelsOverlay(msg.models))
		}
		return m, nil

	case copyResultMsg:
		if msg.err != nil {
			m.errText = "copy failed: " + msg.err.Error()
			m.refreshViewport()
		} else {
			m.notice = fmt.Sprintf("✓ copied last reply (%d chars)", msg.chars)
		}
		return m, nil

	case spinner.TickMsg:
		m.frame++
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.updateSuggestions()
	m.syncInputHeight()
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// updateOverlay handles keys while an overlay (/help, /models, …) is open:
// esc/enter/q close it, arrows scroll it, everything else is swallowed.
func (m *Model) updateOverlay(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m.handleCtrlC()
	case tea.KeyEsc, tea.KeyEnter:
		m.closeOverlay()
		return m, nil
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	if msg.String() == "q" {
		m.closeOverlay()
	}
	return m, nil
}

func (m *Model) send() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" && len(m.attachments) == 0 {
		return nil
	}
	m.input.Reset()
	m.errText = ""
	m.notice = ""
	images := m.attachments
	m.attachments = nil
	m.syncInputHeight()
	m.relayout()
	m.sentCount++
	m.toolDepth = 0 // a fresh user turn gets a fresh tool budget
	return m.dispatch(text, images)
}

// maybeRunTools executes any tool blocks in the newest assistant reply and
// feeds the results back to the model, bounded by tools.max_iterations per
// user turn so a confused model cannot loop forever.
func (m *Model) maybeRunTools() tea.Cmd {
	if !m.toolsOn || m.toolRunner == nil {
		return nil
	}
	n := len(m.session.Messages)
	if n == 0 || m.session.Messages[n-1].Role != provider.RoleAssistant {
		return nil
	}
	calls := tools.Parse(m.session.Messages[n-1].Content)
	if len(calls) == 0 {
		return nil
	}
	maxIter := m.cfg.Tools.MaxIterations
	if maxIter <= 0 {
		maxIter = 4
	}
	if m.toolDepth >= maxIter {
		m.errText = fmt.Sprintf("tool loop stopped after %d rounds (tools.max_iterations)", maxIter)
		m.refreshViewport()
		return nil
	}
	m.toolDepth++
	results := make([]tools.Result, 0, len(calls))
	for _, c := range calls {
		results = append(results, m.toolRunner.Execute(c))
	}
	m.notice = fmt.Sprintf("⚒ ran %d tool call(s) — round %d/%d", len(calls), m.toolDepth, maxIter)
	// Results must reach the model, not a stale cached reply.
	m.bypassCache = true
	return m.dispatch(tools.FormatResults(results), nil)
}

// retryLast re-sends the last user message with current settings.
func (m *Model) retryLast() tea.Cmd {
	if m.lastUserMsg == "" {
		m.errText = "nothing to retry yet"
		m.refreshViewport()
		return nil
	}
	if m.thinking {
		m.errText = "a request is already running (esc to stop it first)"
		m.refreshViewport()
		return nil
	}
	// Drop the previous attempt's user message if it got no reply, so the
	// conversation doesn't contain the question twice.
	if n := len(m.session.Messages); n > 0 {
		last := m.session.Messages[n-1]
		if last.Role == provider.RoleUser && last.Content == m.lastUserMsg {
			m.session.Messages = m.session.Messages[:n-1]
		}
	}
	m.notice = "retrying last message"
	m.sentCount++
	return m.dispatch(m.lastUserMsg, m.lastImages)
}

type firstStreamMsg struct {
	stream  <-chan provider.ChatEvent
	event   provider.ChatEvent
	ok      bool
	retries int
}

func (m *Model) pasteImage() tea.Cmd {
	if !provider.SupportsVision(m.model) && !m.cfg.Chat.ForceVision {
		m.errText = fmt.Sprintf("model %q does not appear to support images (set chat.force_vision: true to override)", m.model)
		m.refreshViewport()
		return nil
	}
	return func() tea.Msg {
		data, mime, err := clipboard.ReadImage(context.Background())
		if err != nil {
			return clipboardImageMsg{err: err}
		}
		return clipboardImageMsg{img: provider.Image{Data: data, MIME: mime}}
	}
}

func (m *Model) hasUserContent() bool {
	for _, msg := range m.session.Messages {
		if msg.Role == provider.RoleUser {
			return true
		}
	}
	return false
}

// saveSession writes the conversation to the history directory. The same
// session name is reused for the whole chat, so repeated saves update
// one file instead of scattering copies.
func (m *Model) saveSession() (string, error) {
	if m.historyDir == "" {
		return "", fmt.Errorf("history saving is disabled (chat.save_history)")
	}
	return history.Save(m.historyDir, m.sessionName, m.sessionRecord())
}

func (m *Model) saveWithNotice() {
	path, err := m.saveSession()
	if err != nil {
		m.errText = "save failed: " + err.Error()
		m.refreshViewport()
		return
	}
	m.savedPath = path
	m.notice = "✓ session saved to " + path
}

// ctrlCWindow is how long the first Ctrl+C stays armed for the second.
const ctrlCWindow = 2 * time.Second

// handleCtrlC implements two-step quit: the first press stops generation or
// clears the input, the second press within the window exits (auto-saving).
func (m *Model) handleCtrlC() (tea.Model, tea.Cmd) {
	if time.Since(m.ctrlCAt) < ctrlCWindow {
		return m, m.quit()
	}
	m.ctrlCAt = time.Now()
	switch {
	case m.thinking && m.cancelStream != nil:
		m.cancelStream()
		m.finishStream(nil)
		m.errText = "generation stopped"
		m.notice = "press ctrl+c again to exit"
		m.refreshViewport()
	case m.input.Value() != "":
		m.input.Reset()
		m.updateSuggestions()
		m.syncInputHeight()
		m.notice = "input cleared — press ctrl+c again to exit"
	default:
		m.notice = "press ctrl+c again to exit (session auto-saves)"
	}
	return m, nil
}

// quit stops any stream, auto-saves the session, and exits.
func (m *Model) quit() tea.Cmd {
	if m.cancelStream != nil {
		m.cancelStream()
	}
	if m.historyDir != "" && m.hasUserContent() {
		if path, err := m.saveSession(); err == nil { // best effort on exit
			m.savedPath = path
		}
	}
	return tea.Quit
}

// wrapLines estimates how many rows value occupies at the given wrap width.
func wrapLines(value string, width int) int {
	if width < 1 {
		width = 1
	}
	lines := 0
	for _, l := range strings.Split(value, "\n") {
		lines += 1 + utf8.RuneCountInString(l)/width
	}
	const maxInputLines = 6
	if lines < 1 {
		lines = 1
	}
	if lines > maxInputLines {
		lines = maxInputLines
	}
	return lines
}

// syncInputHeight grows and shrinks the input box with its content,
// Claude-Code style: 1 row when empty, up to 6 rows for long prompts.
func (m *Model) syncInputHeight() {
	lines := wrapLines(m.input.Value(), m.width-8)
	if lines != m.inputLines {
		m.inputLines = lines
		m.input.SetHeight(lines)
		m.relayout()
	}
}

// copyLastReply copies the most recent assistant text (or the in-flight
// stream) to the system clipboard as raw Markdown.
func (m *Model) copyLastReply() tea.Cmd {
	text := ""
	if m.thinking && m.streamBuf.Len() > 0 {
		text = m.streamBuf.String()
	} else {
		for i := len(m.session.Messages) - 1; i >= 0; i-- {
			if m.session.Messages[i].Role == provider.RoleAssistant {
				text = m.session.Messages[i].Content
				break
			}
		}
	}
	if text == "" {
		m.notice = "nothing to copy yet"
		return nil
	}
	return func() tea.Msg {
		err := clipboard.WriteText(context.Background(), text)
		return copyResultMsg{chars: len(text), err: err}
	}
}

func (m *Model) handleStreamEvent(msg streamEventMsg) (tea.Model, tea.Cmd) {
	if !m.thinking {
		// Stream already finalized (e.g. stopped with Esc); drop late events.
		return m, nil
	}
	if !msg.ok {
		// Channel closed without a terminal event. If the inactivity watchdog
		// tripped, say so; otherwise treat it as a clean finish.
		if m.streamCanceledByIdle() {
			m.streamFailed(m.idleError())
		} else {
			m.finishStream(nil)
		}
		return m, nil
	}
	switch msg.event.Type {
	case provider.EventReasoning:
		// The model is thinking (reasoning_content). It produces no visible
		// answer yet, but it is active — reset the idle deadline and show a
		// live indicator so a long thinking phase never looks frozen or times
		// out.
		m.reasoningLen += len(msg.event.Delta)
		if m.idleWatchdog != nil {
			m.idleWatchdog.Reset(m.idleTimeout)
		}
		m.refreshViewport()
		return m, waitForEvent(m.stream)
	case provider.EventDelta:
		m.streamBuf.WriteString(msg.event.Delta)
		// A token arrived: the stream is healthy, so push the idle deadline out.
		if m.idleWatchdog != nil {
			m.idleWatchdog.Reset(m.idleTimeout)
		}
		m.refreshViewport()
		return m, waitForEvent(m.stream)
	case provider.EventDone:
		m.finishStream(msg.event.Usage)
		// Tools only run on a clean finish, never on Esc/Ctrl+C partials.
		if cmd := m.maybeRunTools(); cmd != nil {
			return m, cmd
		}
		return m, nil
	case provider.EventError:
		// A cancellation caused by the idle watchdog surfaces here as
		// context.Canceled; report it as a stall, not a raw cancel.
		if m.streamCanceledByIdle() {
			m.streamFailed(m.idleError())
		} else {
			m.streamFailed(msg.event.Err)
		}
		return m, nil
	}
	return m, nil
}

// streamCanceledByIdle reports whether the current stream's context was
// canceled by the inactivity watchdog rather than by the user.
func (m *Model) streamCanceledByIdle() bool {
	return m.streamCtx != nil && errors.Is(context.Cause(m.streamCtx), errStreamIdle)
}

func (m *Model) idleError() error {
	return fmt.Errorf("no response from %s for %s — the model may be stuck, or raise network.timeout if it just needs more time",
		m.prov.Name(), m.idleTimeout)
}

// streamFailed finalizes a failed stream, preserving partial output.
func (m *Model) streamFailed(err error) {
	m.thinking = false
	m.errText = err.Error()
	// Preserve partial streamed output instead of discarding it.
	if partial := m.streamBuf.String(); partial != "" {
		m.session.AddAssistant(partial)
		m.replyCount++
		m.streamBuf.Reset()
		m.errText += " (partial reply kept)"
	}
	if m.cancelStream != nil {
		m.cancelStream()
		m.cancelStream = nil
	}
	m.idleWatchdog = nil
	m.drainStream()
	m.refreshViewport()
}

// drainStream consumes any remaining events of an abandoned stream in the
// background. The provider goroutine may still be blocked sending; reading
// until the channel closes lets it exit and release its HTTP connection.
func (m *Model) drainStream() {
	if m.stream == nil {
		return
	}
	go func(s <-chan provider.ChatEvent) {
		for range s {
		}
	}(m.stream)
	m.stream = nil
}

func (m *Model) finishStream(usage *provider.Usage) {
	m.thinking = false
	reply := m.streamBuf.String()
	m.streamBuf.Reset()
	if m.cancelStream != nil {
		m.cancelStream()
		m.cancelStream = nil
	}
	m.idleWatchdog = nil
	m.drainStream()
	if reply != "" {
		m.session.AddAssistant(reply)
		m.replyCount++
	}
	// Cache the successful response (never failures or empty replies).
	// Key, provider, and model come from the dispatch-time snapshot: the user
	// may have run /model or /provider while this reply was streaming.
	if reply != "" && usage != nil && m.responseCache != nil && m.responseCache.Enabled() &&
		m.lastDebug.CacheStatus != "bypass" &&
		len(m.lastImages) == 0 && (!m.lastDebug.Stream || m.cfg.Cache.CacheStreamedResponses) {
		if err := m.responseCache.Put(m.lastDebug.CacheKey, cache.Entry{
			Response:         reply,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			Estimated:        usage.Estimated,
			Provider:         m.lastDebug.Provider,
			Model:            m.lastDebug.Model,
		}); err == nil {
			m.lastDebug.CacheStatus = "write"
		}
	}

	if usage != nil {
		duration := time.Since(m.streamStart)
		st := m.session.RecordUsage(*usage, duration)
		m.lastTPS = st.TokensPerSec
		m.lastDebug.Duration = duration
		m.lastDebug.Usage = usage
		// Attribute to the dispatch-time provider/model for the exit summary.
		m.recordModelUsage(m.lastDebug.Provider, m.lastDebug.Model,
			usage.PromptTokens, usage.CompletionTokens, usage.Estimated, duration)
		if m.debugMode {
			m.notice = fmt.Sprintf("debug: %s · prompt %d · reply %d · cache %s · retries %d — /debug last",
				duration.Round(10*time.Millisecond), usage.PromptTokens, usage.CompletionTokens,
				m.lastDebug.CacheStatus, m.lastDebug.Retries)
		}
		if m.historyDir != "" {
			// Best effort: stats must never interrupt the chat. Attribution
			// uses the dispatch-time snapshot, not the current selection.
			_ = history.AppendUsage(m.historyDir, history.UsageRecord{
				Time:             time.Now(),
				Provider:         m.lastDebug.Provider,
				Model:            m.lastDebug.Model,
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				DurationMS:       duration.Milliseconds(),
				Estimated:        usage.Estimated,
			})
		}
	}
	m.refreshViewport()
}

// relayout recomputes panel heights after non-resize layout changes
// (e.g. attachment chips appearing above the input).
func (m *Model) relayout() {
	if m.ready {
		m.resize(m.width, m.height)
	}
}

func (m *Model) resize(w, h int) {
	m.width, m.height = w, h

	m.input.SetWidth(w - 6)

	// Layout: viewport fills space above usage panel (4), suggestion popup,
	// input (border + content rows, +1 when attachment chips are shown),
	// status bar (1), and help footer (1).
	inputHeight := 2 + m.inputLines
	if len(m.attachments) > 0 {
		inputHeight++
	}
	vpHeight := h - 4 - len(m.sugs) - inputHeight - 1 - 1
	if vpHeight < 3 {
		vpHeight = 3
	}
	if !m.ready {
		m.viewport = viewport.New(w, vpHeight)
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = vpHeight
	}

	renderWidth := w - 4
	if renderWidth < 20 {
		renderWidth = 20
	}
	if renderWidth != m.renderWidth {
		m.renderWidth = renderWidth
		// A fixed standard style avoids WithAutoStyle's terminal query,
		// which can stall the update loop on terminals that never answer.
		style := "light"
		if lipgloss.HasDarkBackground() {
			style = "dark"
		}
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle(style),
			glamour.WithWordWrap(renderWidth),
		)
		if err == nil {
			m.renderer = r
		}
	}
	m.refreshViewport()
}

func (m *Model) renderMarkdown(s string) string {
	if !m.cfg.UI.Markdown || m.renderer == nil {
		return s
	}
	out, err := m.renderer.Render(s)
	if err != nil {
		return s
	}
	return strings.TrimRight(out, "\n") + "\n"
}

func (m *Model) refreshViewport() {
	// While an overlay is showing, the viewport belongs to it; async events
	// (health results, stream deltas) must not stomp its content. The chat
	// re-renders when the overlay closes.
	if !m.ready || m.overlayOpen {
		return
	}
	var b strings.Builder

	if m.demoMode {
		b.WriteString(m.theme.SystemNote.Render("⚠ no backend reachable — running in offline demo mode (mock provider)"))
		b.WriteString("\n\n")
	}

	for _, msg := range m.session.Messages {
		switch msg.Role {
		case provider.RoleUser:
			// Tool results travel as user messages; style them as machinery,
			// not as something the human typed.
			if strings.HasPrefix(msg.Content, tools.ResultsPrefix) {
				b.WriteString(m.theme.SystemNote.Render("⚒ tools"))
				b.WriteString("\n")
				b.WriteString(m.theme.SystemNote.Render(msg.Content))
				b.WriteString("\n\n")
				continue
			}
			b.WriteString(m.theme.UserLabel.Render("you"))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(m.theme.Text).Render(msg.Content))
			for i := range msg.Images {
				if i == 0 && msg.Content != "" {
					b.WriteString(" ")
				}
				b.WriteString(m.theme.SystemNote.Render(fmt.Sprintf("⌗ [image %d] ", i+1)))
			}
			b.WriteString("\n\n")
		case provider.RoleAssistant:
			b.WriteString(m.theme.AssistantLabel.Render("assistant"))
			b.WriteString("\n")
			b.WriteString(m.renderMarkdown(msg.Content))
			b.WriteString("\n")
		}
	}

	if m.thinking {
		b.WriteString(m.theme.AssistantLabel.Render("assistant"))
		b.WriteString("\n")
		switch {
		case m.streamBuf.Len() > 0:
			b.WriteString(m.streamBuf.String())
			b.WriteString("\n")
		case m.reasoningLen > 0:
			// Reasoning model is still thinking; show progress so the wait
			// is visible rather than a frozen screen.
			b.WriteString(m.theme.SystemNote.Render(
				fmt.Sprintf("thinking… (%s of reasoning so far)", components.FormatTokens(m.reasoningLen/4))))
			b.WriteString("\n")
		}
	}

	if m.errText != "" {
		b.WriteString(m.theme.ErrorText.Render("✗ " + m.errText))
		b.WriteString("\n")
	}

	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(b.String()))
	m.viewport.GotoBottom()
}

// View renders the full screen.
func (m *Model) View() string {
	if !m.ready {
		return "loading…"
	}

	usage := components.UsagePanel(m.theme, components.UsagePanelData{
		TokenHistory: m.session.TokenHistory(),
		PromptTotal:  m.session.TotalPromptTokens,
		ReplyTotal:   m.session.TotalCompletionTokens,
		Estimated:    m.session.AnyEstimated,
	}, m.width)

	inputContent := m.input.View()
	if len(m.attachments) > 0 {
		chips := make([]string, len(m.attachments))
		for i, img := range m.attachments {
			chips[i] = m.theme.Badge.Render(fmt.Sprintf("⌗ image %d", i+1)) +
				m.theme.HelpFooter.Render(fmt.Sprintf(" %.0f KB · ctrl+x remove", float64(len(img.Data))/1024))
		}
		inputContent = strings.Join(chips, "   ") + "\n" + inputContent
	}
	inputView := m.theme.InputPanel.Width(m.width - 2).Render(inputContent)

	prof, _ := m.activeProfile()
	profileLabel := prof.Name
	if m.profileMode == "auto" || m.profileMode == "" {
		profileLabel = "auto/" + prof.Name
	}
	ctxWindow, _ := m.contextWindow()
	status := components.StatusBar(m.theme, components.StatusBarData{
		Provider:     m.prov.Name(),
		Model:        m.model,
		Connected:    m.connected,
		DemoMode:     m.demoMode,
		TotalTokens:  m.session.TotalTokens(),
		LastTPS:      m.lastTPS,
		Estimated:    m.session.AnyEstimated,
		Profile:      profileLabel,
		PromptMode:   m.effectivePromptMode(),
		Template:     m.template,
		ContextUsed:  contextmgr.EstimateTokens(m.session.Messages),
		ContextLimit: ctxWindow,
		CacheOn:      m.responseCache != nil && m.responseCache.Enabled(),
		SummaryOn:    m.summary != "",
		ToolsOn:      m.toolsOn,
	}, m.width)

	help := m.theme.HelpFooter.Render("/ commands · /help shortcuts · enter send · ctrl+y copy · ctrl+o select · ctrl+c ×2 quit")
	if m.notice != "" {
		help = m.theme.BadgeOK.Render(m.notice)
	}
	if m.thinking {
		elapsed := fmt.Sprintf("%.1fs", time.Since(m.streamStart).Seconds())
		help = m.spinner.View() + " " +
			components.WorkingButton(m.theme, m.frame, elapsed) + " " +
			components.StopButton(m.theme, m.frame) + "  " +
			m.theme.HelpFooter.Render("ctrl+c quit")
	}

	sections := []string{m.viewport.View(), usage}
	if len(m.sugs) > 0 {
		sections = append(sections, m.suggestionsView())
	}
	sections = append(sections, inputView, status,
		lipgloss.NewStyle().MaxWidth(m.width).Render(help))
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// Run starts the chat TUI and blocks until it exits.
func Run(opts Options) error {
	m := New(opts)

	// Ask the terminal to report modified Enter (Shift+Enter, Ctrl+Enter)
	// via modifyOtherKeys; unsupported terminals ignore this sequence.
	fmt.Print(enableModifyOtherKeys)
	defer fmt.Print(disableModifyOtherKeys)

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := p.Run()
	if err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	// The alt screen is gone now; leave the session report in the scrollback,
	// the way modern agent CLIs sign off.
	if fm, ok := final.(*Model); ok {
		fmt.Println(renderExitSummary(fm.theme, fm.exitSummary()))
	}
	return nil
}
