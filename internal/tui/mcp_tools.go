package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// defaultMCPTimeout bounds one MCP call when a server has no configured
// timeout (mirrors tools.Runner's own 30s default for run_command).
const defaultMCPTimeout = 30 * time.Second

// mcpToolResultsMsg carries the ordered results of an async tool batch that
// contained at least one MCP call (see runMixedToolBatch).
type mcpToolResultsMsg struct {
	results []tools.Result
}

// mcpToolSpecs converts every connected, enabled MCP server's tools into
// native function-calling specs, named "mcp__<server>__<tool>" so multiple
// servers can never collide on name. Returns nil if mcpReg is nil or no
// server is currently connected.
func mcpToolSpecs(mcpReg *mcp.Registry) []provider.ToolSpec {
	if mcpReg == nil {
		return nil
	}
	var out []provider.ToolSpec
	for _, srv := range mcpReg.List() {
		if srv.Status != mcp.StatusConnected || !srv.Config.Enabled {
			continue
		}
		for _, t := range srv.Tools {
			out = append(out, provider.ToolSpec{
				Name:        tools.JoinMCPToolName(srv.Config.Name, t.Name),
				Description: t.Description,
				Parameters:  t.Schema,
			})
		}
	}
	return out
}

// mcpServerTimeout resolves the bounded timeout for one server's calls,
// falling back to defaultMCPTimeout when unset or the server is unknown.
func mcpServerTimeout(mcpReg *mcp.Registry, server string) time.Duration {
	if mcpReg == nil {
		return defaultMCPTimeout
	}
	srv, ok := mcpReg.Get(server)
	if !ok || srv.Config.Timeout <= 0 {
		return defaultMCPTimeout
	}
	return srv.Config.Timeout
}

// executeMCPCall runs one MCP call with a bounded timeout, converting the
// result (or any error) into a tools.Result. It never panics: an unknown
// server, a disconnected server, malformed arguments, a timeout, or a
// cancellation all land in Result.Err so the model can see what happened
// and retry, matching the native tools' "the model sees the problem" style.
func executeMCPCall(ctx context.Context, mcpReg *mcp.Registry, c tools.Call) tools.Result {
	res := tools.Result{Call: c}
	if mcpReg == nil {
		res.Err = fmt.Errorf("mcp server %q: MCP is not available", c.MCPServer)
		return res
	}
	timeout := mcpServerTimeout(mcpReg, c.MCPServer)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := c.MCPArgs
	if args == "" {
		args = "{}"
	}
	out, err := mcpReg.CallTool(callCtx, c.MCPServer, c.MCPTool, []byte(args))
	if err != nil {
		switch {
		case errors.Is(callCtx.Err(), context.DeadlineExceeded):
			res.Err = fmt.Errorf("mcp %s.%s timed out after %s", c.MCPServer, c.MCPTool, timeout)
		case errors.Is(ctx.Err(), context.Canceled):
			res.Err = fmt.Errorf("mcp %s.%s cancelled by the user", c.MCPServer, c.MCPTool)
		default:
			res.Err = err
		}
		return res
	}
	res.Output = out.Content
	if out.IsError {
		res.Err = fmt.Errorf("%s", out.Content)
	}
	return res
}

// containsMCPCall reports whether any call in the batch targets an MCP
// server — the signal runToolCalls (Task 7) uses to decide between the
// unchanged synchronous native path and the new async path.
func containsMCPCall(calls []tools.Call) bool {
	for _, c := range calls {
		if c.MCPServer != "" {
			return true
		}
	}
	return false
}

// mcpBatchNotice names the first MCP call in a batch, so the UI shows what
// it's waiting on instead of looking frozen while the async command runs.
func mcpBatchNotice(calls []tools.Call) string {
	for _, c := range calls {
		if c.MCPServer != "" {
			return fmt.Sprintf("⚒ running %s: %s…", c.MCPServer, c.MCPTool)
		}
	}
	return "⚒ running tool call(s)…"
}

// runMixedToolBatch executes a batch that contains at least one MCP call as
// a single async tea.Cmd: every call in the batch runs sequentially, in
// order — including the native ones, so ordering is never split across
// messages — because MCP servers commonly serialize session state
// (jiraWorklog sets allow_parallel: false) and the latency cost of
// sequential execution is negligible next to model-inference time for the
// handful of calls a typical turn makes.
func runMixedToolBatch(ctx context.Context, runner *tools.Runner, mcpReg *mcp.Registry, calls []tools.Call) tea.Cmd {
	return func() tea.Msg {
		results := make([]tools.Result, 0, len(calls))
		for _, c := range calls {
			if c.MCPServer != "" {
				results = append(results, executeMCPCall(ctx, mcpReg, c))
				continue
			}
			results = append(results, runner.Execute(c))
		}
		return mcpToolResultsMsg{results: results}
	}
}

// registerMCPCapabilities adds every connected server's tools into reg so
// /tools list and /tools inspect show them alongside native and web tools —
// the seam internal/tools/registry.go's own DefaultRegistry comment already
// anticipated. Source is "mcp:<server>" so /tools list <filter> can match
// either "mcp" (every server) or one server's exact name.
func registerMCPCapabilities(reg *tools.Registry, mcpReg *mcp.Registry) {
	if mcpReg == nil {
		return
	}
	for _, srv := range mcpReg.List() {
		if srv.Status != mcp.StatusConnected {
			continue
		}
		for _, t := range srv.Tools {
			_ = reg.Register(tools.CapabilityInfo{
				Name:        tools.JoinMCPToolName(srv.Config.Name, t.Name),
				Description: t.Description,
				Source:      "mcp:" + srv.Config.Name,
				Safety:      tools.SafetyExternalMCP,
				Approval:    srv.Config.ApproveMode(),
				Parameters:  t.Schema,
			})
		}
	}
}
