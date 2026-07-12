package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
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

func TestRegistryServerNamesAreCaseInsensitive(t *testing.T) {
	r := NewRegistry([]ServerConfig{{
		Name:      "jiraworklog",
		Enabled:   true,
		Transport: TransportStdio,
		Command:   "jira-mcp",
	}}, NewMockFactory())

	if _, ok := r.Get("jiraWorklog"); !ok {
		t.Fatal("Get did not match server name case-insensitively")
	}
	if err := r.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("Connect with original YAML casing: %v", err)
	}
	if _, err := r.CallTool(context.Background(), "JIRAWORKLOG", "jiraworklog_echo", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("CallTool with uppercase server name: %v", err)
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

func TestMockClientCallToolRespectsContextTimeout(t *testing.T) {
	c := &MockClient{ServerName: "slow", Delay: 50 * time.Millisecond}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.CallTool(ctx, "slow_echo", json.RawMessage(`{}`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error from a call that outlives its context")
	}
	if elapsed > 40*time.Millisecond {
		t.Errorf("CallTool took %s, want it to return promptly once ctx expires (well under the 50ms Delay)", elapsed)
	}
}

// countingFactory wraps NewMockFactory but counts how many clients it builds
// and records every client it hands out, so tests can assert exactly one
// client was created (no leaked duplicate) and check each one's Closed().
func countingFactory() (ClientFactory, *int32, func() []*MockClient) {
	var n int32
	var mu sync.Mutex
	var clients []*MockClient
	f := func(c ServerConfig) (Client, error) {
		atomic.AddInt32(&n, 1)
		mc := &MockClient{
			ServerName: c.Name,
			CannedTools: []Tool{{
				Server: c.Name,
				Name:   c.Name + "_echo",
			}},
		}
		mu.Lock()
		clients = append(clients, mc)
		mu.Unlock()
		return mc, nil
	}
	return f, &n, func() []*MockClient {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*MockClient, len(clients))
		copy(out, clients)
		return out
	}
}

// TestDoubleConnectRejected covers Bug 2: connecting an already-connected
// server must not silently overwrite (and thereby leak) the first client's
// subprocess. The second Connect should fail, exactly one client should ever
// be created, and that client must still be open (owned by the registry).
func TestDoubleConnectRejected(t *testing.T) {
	factory, n, clients := countingFactory()
	r := NewRegistry(sampleConfigs(), factory)

	if err := r.Connect(context.Background(), "files"); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if err := r.Connect(context.Background(), "files"); err == nil {
		t.Fatal("second Connect on an already-connected server succeeded, want error")
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Fatalf("clients created = %d, want 1 (second Connect must not spawn another)", got)
	}
	all := clients()
	if len(all) != 1 || all[0].Closed() {
		t.Fatalf("the single client should remain open and owned by the registry")
	}
	s, _ := r.Get("files")
	if s.Status != StatusConnected {
		t.Errorf("status after rejected double-connect = %q, want connected", s.Status)
	}
}

// TestConcurrentConnectOnlyOneWins covers Bug 4's connect-vs-connect half: two
// goroutines racing Registry.Connect for the same server must not both spawn
// a client. Exactly one call succeeds and commits a client; the other must
// error, and any client it created (if the factory ran before the guard
// could apply) must have been closed rather than left dangling.
func TestConcurrentConnectOnlyOneWins(t *testing.T) {
	release := make(chan struct{})
	var n int32
	var mu sync.Mutex
	var clients []*MockClient
	factory := func(c ServerConfig) (Client, error) {
		atomic.AddInt32(&n, 1)
		mc := &MockClient{ServerName: c.Name, ConnectGate: release}
		mu.Lock()
		clients = append(clients, mc)
		mu.Unlock()
		return mc, nil
	}
	r := NewRegistry(sampleConfigs(), factory)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	for i := range 2 {
		go func(i int) {
			defer wg.Done()
			errs[i] = r.Connect(context.Background(), "files")
		}(i)
	}
	// Give both goroutines a chance to reach the connecting-guard check
	// before either finishes dialing.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent Connects = %d, want exactly 1 (errs=%v)", successes, errs)
	}

	s, _ := r.Get("files")
	if s.Status != StatusConnected {
		t.Fatalf("status = %q, want connected", s.Status)
	}
	// Whichever client lost the race (if the factory ran for both before the
	// in-progress guard kicked in) must not be left open.
	mu.Lock()
	defer mu.Unlock()
	open := 0
	for _, c := range clients {
		if !c.Closed() {
			open++
		}
	}
	if open != 1 {
		t.Errorf("open clients after race = %d, want exactly 1 (no leaked client)", open)
	}
}

// TestDisableDuringConnect covers Bug 4's disable-vs-connect half: if a
// server is disabled while a Connect for it is still dialing/handshaking,
// the in-flight Connect must not resurrect it as connected. The fresh
// client it built must be closed, and the server must stay disabled with no
// client attached.
func TestDisableDuringConnect(t *testing.T) {
	gate := make(chan struct{})
	var built *MockClient
	var mu sync.Mutex
	factory := func(c ServerConfig) (Client, error) {
		mc := &MockClient{ServerName: c.Name, ConnectGate: gate}
		mu.Lock()
		built = mc
		mu.Unlock()
		return mc, nil
	}
	r := NewRegistry(sampleConfigs(), factory)

	connectErr := make(chan error, 1)
	go func() {
		connectErr <- r.Connect(context.Background(), "files")
	}()

	// Wait for the factory to have built its client (Connect is now
	// blocked inside client.Connect on the gate) before disabling.
	deadline := time.After(time.Second)
	for {
		mu.Lock()
		ready := built != nil
		mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Connect to reach the client factory")
		case <-time.After(time.Millisecond):
		}
	}

	if err := r.Disable("files"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	close(gate) // let the in-flight Connect proceed past the (now stale) handshake

	if err := <-connectErr; err == nil {
		t.Fatal("Connect that raced a Disable succeeded, want error")
	}

	mu.Lock()
	defer mu.Unlock()
	if !built.Closed() {
		t.Error("the client built during the disabled connect was not closed")
	}
	s, _ := r.Get("files")
	if s.Status != StatusDisabled {
		t.Errorf("status after disable-during-connect = %q, want disabled", s.Status)
	}
	if s.client != nil {
		t.Error("server has a client attached after disable-during-connect, want nil")
	}
}

// TestRegistryCloseClosesCommittedClient extends the existing Connect
// coverage: once a Connect has committed a client, Registry.Close must
// actually close it (not just forget it), or a subsequent connect/close is
// the only place a leaked subprocess would ever get reaped.
func TestRegistryCloseClosesCommittedClient(t *testing.T) {
	factory, _, clients := countingFactory()
	r := NewRegistry(sampleConfigs(), factory)
	if err := r.Connect(context.Background(), "files"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r.Close()
	all := clients()
	if len(all) != 1 || !all[0].Closed() {
		t.Fatalf("client not closed by Registry.Close: %+v", all)
	}
	s, _ := r.Get("files")
	if s.client != nil {
		t.Error("server still has a client attached after Registry.Close")
	}
}

// TestRegistryCloseIsNilSafe covers app.go's Run(), which unconditionally
// defers m.mcpRegistry.Close() on every exit path including ones where the
// registry might not have been constructed.
func TestRegistryCloseIsNilSafe(t *testing.T) {
	var r *Registry
	r.Close() // must not panic
}
