package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeServer plays a minimal MCP server over the given reader/writer: it
// answers initialize, tools/list, and tools/call, and ignores notifications.
func fakeServer(t *testing.T, in io.Reader, out io.Writer) {
	t.Helper()
	r := bufio.NewReader(in)
	respond := func(id *int, result any) {
		res, _ := json.Marshal(result)
		msg, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Result: res})
		_, _ = out.Write(append(msg, '\n'))
	}
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var req rpcRequest
			if json.Unmarshal(line, &req) == nil {
				switch req.Method {
				case "initialize":
					respond(req.ID, map[string]any{
						"protocolVersion": protocolVersion,
						"serverInfo":      map[string]any{"name": "fake", "version": "1"},
						"capabilities":    map[string]any{},
					})
				case "notifications/initialized":
					// no response for notifications
				case "tools/list":
					respond(req.ID, map[string]any{"tools": []map[string]any{
						{"name": "echo", "description": "echo text", "inputSchema": map[string]any{"type": "object"}},
					}})
				case "tools/call":
					respond(req.ID, map[string]any{
						"content": []map[string]any{{"type": "text", "text": "echoed!"}},
						"isError": false,
					})
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// newPipedClient wires a StdioClient to an in-process fake server, bypassing
// subprocess creation.
func newPipedClient(t *testing.T) *StdioClient {
	t.Helper()
	c2sR, c2sW, _ := os.Pipe() // client -> server
	s2cR, s2cW, _ := os.Pipe() // server -> client
	go fakeServer(t, c2sR, s2cW)
	return &StdioClient{
		cfg:     ServerConfig{Name: "fake", Transport: TransportStdio, Command: "fake"},
		pending: map[int]chan rpcResponse{},
		closed:  make(chan struct{}),
		w:       c2sW,
		r:       bufio.NewReader(s2cR),
	}
}

func TestStdioHandshakeListAndCall(t *testing.T) {
	c := newPipedClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Server != "fake" {
		t.Errorf("tool server = %q, want fake", tools[0].Server)
	}

	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Content != "echoed!" || res.IsError {
		t.Errorf("result = %+v", res)
	}
}

func TestStdioCallAfterCloseFails(t *testing.T) {
	c := newPipedClient(t)
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	c.Close()
	if _, err := c.ListTools(ctx); err == nil {
		t.Error("ListTools succeeded after Close")
	}
}

func TestStdioFactoryRejectsNonStdio(t *testing.T) {
	f := StdioFactory()
	if _, err := f(ServerConfig{Name: "x", Transport: "http", Command: "srv"}); err == nil {
		t.Error("factory accepted a non-stdio transport")
	}
	if _, err := f(ServerConfig{Name: "x", Transport: TransportStdio, Command: ""}); err == nil {
		t.Error("factory accepted an empty command")
	}
}

func TestServerEnvIsMinimalPlusOverrides(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	env := serverEnv(map[string]string{"MY_TOKEN": "abc"})
	var hasPath, hasToken bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			hasPath = true
		}
		if kv == "MY_TOKEN=abc" {
			hasToken = true
		}
	}
	if !hasPath {
		t.Error("serverEnv dropped PATH")
	}
	if !hasToken {
		t.Error("serverEnv dropped the configured override")
	}
}
