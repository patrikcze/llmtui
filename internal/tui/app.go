// Package tui implements the full-screen Bubble Tea chat interface.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/chat"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
	"github.com/patrikcze/llmtui/internal/tui/components"
	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// Options configures the chat UI.
type Options struct {
	Config   *config.Config
	Provider provider.Provider
	Model    string
}

type healthMsg struct{ err error }

type streamEventMsg struct {
	event provider.ChatEvent
	ok    bool
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
}

// New builds the chat model.
func New(opts Options) *Model {
	t := styles.ByName(opts.Config.UI.Theme)

	ta := textarea.New()
	ta.Placeholder = "Ask anything… (Enter to send, Ctrl+C to quit)"
	ta.Prompt = "┃ "
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = t.Spinner

	return &Model{
		cfg:     opts.Config,
		theme:   t,
		prov:    opts.Provider,
		model:   opts.Model,
		session: chat.NewSession(opts.Config.Chat.SystemPrompt),
		input:   ta,
		spinner: sp,
	}
}

// Init starts the spinner and kicks off the provider health check.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, textarea.Blink, m.checkHealth())
}

func (m *Model) checkHealth() tea.Cmd {
	prov := m.prov
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return healthMsg{err: prov.HealthCheck(ctx)}
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

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.cancelStream != nil {
				m.cancelStream()
			}
			return m, tea.Quit
		case tea.KeyCtrlL:
			m.session.Clear()
			m.refreshViewport()
			return m, nil
		case tea.KeyEnter:
			if !m.thinking {
				return m, m.send()
			}
			return m, nil
		}

	case healthMsg:
		if msg.err != nil {
			// Backend unreachable: fall back to the offline demo provider.
			m.connected = false
			m.demoMode = true
			m.prov = mock.New()
			m.model = "demo-model"
		} else {
			m.connected = true
			m.demoMode = false
		}
		m.refreshViewport()
		return m, nil

	case firstStreamMsg:
		m.stream = msg.stream
		return m.handleStreamEvent(streamEventMsg{event: msg.event, ok: msg.ok})

	case streamEventMsg:
		return m.handleStreamEvent(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) send() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	m.input.Reset()
	m.errText = ""
	m.session.AddUser(text)
	m.thinking = true
	m.streamBuf.Reset()
	m.streamStart = time.Now()
	m.refreshViewport()

	req := provider.ChatRequest{
		Model:       m.model,
		Messages:    append([]provider.Message(nil), m.session.Messages...),
		Temperature: m.cfg.Chat.Temperature,
		TopP:        m.cfg.Chat.TopP,
		MaxTokens:   m.cfg.Chat.MaxTokens,
		Stream:      m.cfg.StreamEnabled(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelStream = cancel
	prov := m.prov

	return func() tea.Msg {
		stream, err := prov.Chat(ctx, req)
		if err != nil {
			return streamEventMsg{event: provider.ChatEvent{Type: provider.EventError, Err: err}, ok: true}
		}
		ev, ok := <-stream
		// Stash the channel on the first event via a wrapper message.
		return firstStreamMsg{stream: stream, event: ev, ok: ok}
	}
}

type firstStreamMsg struct {
	stream <-chan provider.ChatEvent
	event  provider.ChatEvent
	ok     bool
}

func (m *Model) handleStreamEvent(msg streamEventMsg) (tea.Model, tea.Cmd) {
	if !msg.ok {
		// Channel closed without a terminal event; treat as done.
		m.finishStream(nil)
		return m, nil
	}
	switch msg.event.Type {
	case provider.EventDelta:
		m.streamBuf.WriteString(msg.event.Delta)
		m.refreshViewport()
		return m, waitForEvent(m.stream)
	case provider.EventDone:
		m.finishStream(msg.event.Usage)
		return m, nil
	case provider.EventError:
		m.thinking = false
		m.errText = msg.event.Err.Error()
		if m.cancelStream != nil {
			m.cancelStream()
			m.cancelStream = nil
		}
		m.refreshViewport()
		return m, nil
	}
	return m, nil
}

func (m *Model) finishStream(usage *provider.Usage) {
	m.thinking = false
	reply := m.streamBuf.String()
	m.streamBuf.Reset()
	if m.cancelStream != nil {
		m.cancelStream()
		m.cancelStream = nil
	}
	if reply != "" {
		m.session.AddAssistant(reply)
	}
	if usage != nil {
		st := m.session.RecordUsage(*usage, time.Since(m.streamStart))
		m.lastTPS = st.TokensPerSec
	}
	m.refreshViewport()
}

func (m *Model) resize(w, h int) {
	m.width, m.height = w, h

	m.input.SetWidth(w - 6)

	// Layout: viewport fills space above usage panel (4), input (3),
	// status bar (1), and help footer (1).
	vpHeight := h - 4 - 3 - 1 - 1
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
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(renderWidth),
	)
	if err == nil {
		m.renderer = r
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
	if !m.ready {
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
			b.WriteString(m.theme.UserLabel.Render("you"))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(m.theme.Text).Render(msg.Content))
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
		if m.streamBuf.Len() > 0 {
			b.WriteString(m.streamBuf.String())
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

	inputView := m.theme.InputPanel.Width(m.width - 2).Render(m.input.View())

	status := components.StatusBar(m.theme, components.StatusBarData{
		Provider:    m.prov.Name(),
		Model:       m.model,
		Connected:   m.connected,
		DemoMode:    m.demoMode,
		TotalTokens: m.session.TotalTokens(),
		LastTPS:     m.lastTPS,
		Estimated:   m.session.AnyEstimated,
	}, m.width)

	help := m.theme.HelpFooter.Render("enter send · ctrl+l clear · ctrl+c quit")
	if m.thinking {
		help = m.spinner.View() + " " + m.theme.SystemNote.Render("thinking…") + "  " + help
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		m.viewport.View(),
		usage,
		inputView,
		status,
		help,
	)
}

// Run starts the chat TUI and blocks until it exits.
func Run(opts Options) error {
	m := New(opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	if err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	return nil
}
