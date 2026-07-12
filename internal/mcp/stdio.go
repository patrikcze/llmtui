package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/patrikcze/llmtui/internal/procutil"
)

// protocolVersion is the MCP revision llmtui speaks during initialize.
const protocolVersion = "2024-11-05"

// clientName / clientVersion identify llmtui to servers in the handshake.
const (
	clientName    = "llmtui"
	clientVersion = "0.8.0"
)

// StdioClient speaks MCP (JSON-RPC 2.0 over newline-delimited stdio) to a
// subprocess. Requests and responses are correlated by id via a background
// reader goroutine, so concurrent calls are safe.
type StdioClient struct {
	cfg ServerConfig
	cmd *exec.Cmd
	w   io.WriteCloser
	r   *bufio.Reader

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResponse
	writeMu sync.Mutex

	closeOnce sync.Once
	closed    chan struct{}
	readErr   error
	stderr    lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// StdioFactory returns a ClientFactory that builds stdio MCP clients. It is
// the transport a registry uses to actually connect to servers.
func StdioFactory() ClientFactory {
	return func(c ServerConfig) (Client, error) {
		if c.Transport != TransportStdio {
			return nil, fmt.Errorf("mcp: unsupported transport %q (only %q)", c.Transport, TransportStdio)
		}
		if c.Command == "" {
			return nil, fmt.Errorf("mcp: server %q has no command", c.Name)
		}
		return &StdioClient{cfg: c, pending: map[int]chan rpcResponse{}, closed: make(chan struct{})}, nil
	}
}

// --- JSON-RPC framing --------------------------------------------------------

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// --- lifecycle ---------------------------------------------------------------

// Connect starts the subprocess, begins reading, and performs the MCP
// initialize handshake. Starting the process runs the configured command.
func (c *StdioClient) Connect(ctx context.Context) error {
	if c.cmd == nil && c.w == nil { // real process (tests inject w/r directly)
		cmd := exec.Command(c.cfg.Command, c.cfg.Args...)
		env, err := serverEnv(c.cfg.Env)
		if err != nil {
			return fmt.Errorf("mcp: resolve environment: %w", err)
		}
		cmd.Env = env
		procutil.SetupProcAttr(cmd)
		// A descendant that leaves the process group (setsid) survives the
		// group kill and can hold the inherited stderr pipe open forever;
		// without this bound cmd.Wait — and therefore Close — never returns.
		cmd.WaitDelay = 3 * time.Second
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("mcp: stdin pipe: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("mcp: stdout pipe: %w", err)
		}
		// MCP data travels on stdout. Capture stderr for bounded, redacted startup
		// diagnostics instead of discarding the cause when a child exits early.
		cmd.Stderr = &c.stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("mcp: start %q: %w", c.cfg.Command, err)
		}
		c.cmd = cmd
		c.w = stdin
		c.r = bufio.NewReader(stdout)
	}
	go c.readLoop()

	if err := c.handshake(ctx); err != nil {
		_ = c.Close()
		return c.withServerStderr(err)
	}
	return nil
}

var stderrSecretPattern = regexp.MustCompile(`(?i)((?:token|secret|password|authorization|api[_-]?key)[^=:\s]*\s*[=:]\s*)\S+`)

func (c *StdioClient) withServerStderr(err error) error {
	msg := strings.TrimSpace(c.stderr.String())
	if msg == "" {
		return err
	}
	msg = stderrSecretPattern.ReplaceAllString(msg, `${1}***REDACTED***`)
	const max = 4096
	if len(msg) > max {
		msg = msg[len(msg)-max:]
	}
	return fmt.Errorf("%w; server stderr: %s", err, msg)
}

// handshake performs initialize + notifications/initialized.
func (c *StdioClient) handshake(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName, "version": clientVersion},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("mcp: initialize: %w", err)
	}
	if err := c.notify("notifications/initialized", nil); err != nil {
		return fmt.Errorf("mcp: initialized notification: %w", err)
	}
	return nil
}

// ListTools issues tools/list.
func (c *StdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("mcp: decode tools/list: %w", err)
	}
	out := make([]Tool, 0, len(payload.Tools))
	for _, t := range payload.Tools {
		out = append(out, Tool{
			Server:      c.cfg.Name,
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.InputSchema,
		})
	}
	return out, nil
}

// CallTool issues tools/call and flattens text content into a Result.
func (c *StdioClient) CallTool(ctx context.Context, name string, input json.RawMessage) (Result, error) {
	args := input
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	params := map[string]any{"name": name, "arguments": args}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return Result{}, err
	}
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Result{}, fmt.Errorf("mcp: decode tools/call: %w", err)
	}
	var b strings.Builder
	for _, part := range payload.Content {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	return Result{Content: b.String(), IsError: payload.IsError}, nil
}

// Close closes stdin, then terminates the process (and, on Unix, its process
// group, so wrapper commands like npx/uvx/sh -c don't leave grandchildren
// running), and unblocks callers.
func (c *StdioClient) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.w != nil {
			_ = c.w.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			procutil.Terminate(c.cmd)
		}
	})
	return nil
}

// --- request/response plumbing ----------------------------------------------

// call sends a request and waits for the correlated response, the context, or
// connection close.
func (c *StdioClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		if c.readErr != nil {
			return nil, c.readErr
		}
		return nil, fmt.Errorf("mcp: connection closed")
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// notify sends a request with no id (a JSON-RPC notification).
func (c *StdioClient) notify(method string, params any) error {
	return c.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

// write serializes one message as a single newline-delimited line.
func (c *StdioClient) write(msg rpcRequest) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("mcp: write: %w", err)
	}
	return nil
}

// readLoop reads newline-delimited responses and routes them to callers by id.
// Notifications from the server (no matching pending id) are ignored.
func (c *StdioClient) readLoop() {
	for {
		line, err := c.r.ReadBytes('\n')
		if len(line) > 0 {
			var resp rpcResponse
			if json.Unmarshal(line, &resp) == nil && resp.ID != nil {
				c.mu.Lock()
				ch, ok := c.pending[*resp.ID]
				c.mu.Unlock()
				if ok {
					ch <- resp
				}
			}
		}
		if err != nil {
			c.mu.Lock()
			if c.readErr == nil && err != io.EOF {
				c.readErr = fmt.Errorf("mcp: read: %w", err)
			}
			c.mu.Unlock()
			c.Close()
			return
		}
	}
}

// serverEnv builds the subprocess environment: a small safe base plus the
// user-configured overrides. The full host environment is not inherited, so
// unrelated host secrets do not leak into the server.
func serverEnv(overrides map[string]string) ([]string, error) {
	base := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "USER", "SHELL", "LANG", "LC_ALL", "TMPDIR", "TERM"} {
		if v, ok := os.LookupEnv(key); ok {
			base[key] = v
		}
	}
	for k, ref := range overrides {
		v, err := resolveEnvValue(ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		// Viper normalizes map keys to lowercase while unmarshalling. For the
		// common pass-through form FOO: env:FOO, recover the exact (and on Unix,
		// case-sensitive) destination name from the reference.
		destination := k
		if name, ok := strings.CutPrefix(ref, "env:"); ok && strings.EqualFold(k, name) {
			destination = name
		}
		base[destination] = v
	}
	out := make([]string, 0, len(base))
	for k, v := range base {
		out = append(out, k+"="+v)
	}
	return out, nil
}

// resolveEnvValue lets MCP configuration refer to a secret without storing it
// in llmtui's YAML. Plain values remain supported for backwards compatibility.
func resolveEnvValue(ref string) (string, error) {
	if name, ok := strings.CutPrefix(ref, "env:"); ok {
		if name == "" {
			return "", fmt.Errorf("empty env reference")
		}
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			return "", fmt.Errorf("environment variable %q is not set", name)
		}
		return v, nil
	}
	if path, ok := strings.CutPrefix(ref, "file:"); ok {
		if path == "" {
			return "", fmt.Errorf("empty file reference")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read secret file: %w", err)
		}
		v := strings.TrimRight(string(b), "\r\n")
		if v == "" {
			return "", fmt.Errorf("secret file is empty")
		}
		return v, nil
	}
	return ref, nil
}
