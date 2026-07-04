package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func sampleConfigs() []ServerConfig {
	return []ServerConfig{
		{Name: "files", Enabled: true, Transport: TransportStdio, Command: "mcp-fs", Approve: ApproveAsk, Timeout: 30 * time.Second},
		{Name: "disabled", Enabled: false, Transport: TransportStdio, Command: "mcp-x"},
	}
}

func TestValidate(t *testing.T) {
	ok := ServerConfig{Name: "a", Transport: TransportStdio, Command: "srv"}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	bad := []ServerConfig{
		{Name: "", Transport: TransportStdio, Command: "srv"},
		{Name: "a", Transport: "", Command: "srv"},
		{Name: "a", Transport: "carrier-pigeon", Command: "srv"},
		{Name: "a", Transport: TransportStdio, Command: ""},
		{Name: "a", Transport: TransportStdio, Command: "srv", Approve: "maybe"},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("bad config %d passed validation", i)
		}
	}
}

func TestRedactedEnv(t *testing.T) {
	c := ServerConfig{Name: "a", Env: map[string]string{"API_TOKEN": "supersecret", "PORT": "8080"}}
	red := c.RedactedEnv()
	for k, v := range red {
		if v != "***" {
			t.Errorf("env %q not redacted: %q", k, v)
		}
	}
	// The original must be untouched.
	if c.Env["API_TOKEN"] != "supersecret" {
		t.Error("RedactedEnv mutated the original env")
	}
}

func TestRegistryDisabledByDefault(t *testing.T) {
	// A nil factory means no transport: enabled servers report NoTransport,
	// disabled ones stay Disabled. Either way nothing connects.
	r := NewRegistry(sampleConfigs(), nil)
	files, _ := r.Get("files")
	if files.Status != StatusNoTransport {
		t.Errorf("enabled server status = %q, want no_transport (nil factory)", files.Status)
	}
	dis, _ := r.Get("disabled")
	if dis.Status != StatusDisabled {
		t.Errorf("disabled server status = %q, want disabled", dis.Status)
	}
}

func TestConnectWithoutTransportFails(t *testing.T) {
	r := NewRegistry(sampleConfigs(), nil)
	if err := r.Connect(context.Background(), "files"); err == nil {
		t.Fatal("Connect succeeded with no transport")
	}
}

func TestConnectWithMockFactory(t *testing.T) {
	r := NewRegistry(sampleConfigs(), NewMockFactory())
	files, _ := r.Get("files")
	if files.Status != StatusConfigured {
		t.Fatalf("status with factory = %q, want configured", files.Status)
	}
	if err := r.Connect(context.Background(), "files"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	files, _ = r.Get("files")
	if files.Status != StatusConnected {
		t.Errorf("status after connect = %q, want connected", files.Status)
	}
	if len(files.Tools) != 1 || files.Tools[0].Name != "files_echo" {
		t.Errorf("tools = %+v", files.Tools)
	}
	// Registry-wide tool list.
	if len(r.Tools()) != 1 {
		t.Errorf("Tools() = %d, want 1", len(r.Tools()))
	}
}

func TestConnectDisabledServerFails(t *testing.T) {
	r := NewRegistry(sampleConfigs(), NewMockFactory())
	if err := r.Connect(context.Background(), "disabled"); err == nil {
		t.Error("connected a disabled server")
	}
}

func TestEnableDisableLifecycle(t *testing.T) {
	r := NewRegistry(sampleConfigs(), NewMockFactory())
	if err := r.Enable("disabled"); err != nil {
		t.Fatal(err)
	}
	if err := r.Connect(context.Background(), "disabled"); err != nil {
		t.Fatalf("connect after enable: %v", err)
	}
	s, _ := r.Get("disabled")
	if s.Status != StatusConnected {
		t.Errorf("status = %q, want connected", s.Status)
	}
	// Disable must close the connection and reset state.
	if err := r.Disable("disabled"); err != nil {
		t.Fatal(err)
	}
	s, _ = r.Get("disabled")
	if s.Status != StatusDisabled || len(s.Tools) != 0 {
		t.Errorf("after disable: status=%q tools=%d", s.Status, len(s.Tools))
	}
}

func TestCallToolRouting(t *testing.T) {
	r := NewRegistry(sampleConfigs(), NewMockFactory())
	_ = r.Connect(context.Background(), "files")
	res, err := r.CallTool(context.Background(), "files", "files_echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Content == "" {
		t.Error("empty result content")
	}
	// Unknown server errors rather than panics.
	if _, err := r.CallTool(context.Background(), "nope", "x", nil); err == nil {
		t.Error("call to unknown server did not error")
	}
}

func TestConnectErrorRecorded(t *testing.T) {
	factory := func(c ServerConfig) (Client, error) {
		return &MockClient{ServerName: c.Name, ConnectErr: context.DeadlineExceeded}, nil
	}
	r := NewRegistry(sampleConfigs(), factory)
	if err := r.Connect(context.Background(), "files"); err == nil {
		t.Fatal("expected connect error")
	}
	s, _ := r.Get("files")
	if s.Status != StatusError || s.LastErr == nil {
		t.Errorf("error not recorded: status=%q err=%v", s.Status, s.LastErr)
	}
}
