package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
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
		// "__" is the server/tool separator in model-visible tool names
		// ("mcp__<server>__<tool>"); allowing it in a server name would make
		// SplitMCPToolName ambiguous and could misroute a call.
		{Name: "my__server", Transport: TransportStdio, Command: "srv"},
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

func TestStatusConnectingDuringDial(t *testing.T) {
	gate := make(chan struct{})
	factory, _ := trackingFactory(func(c *MockClient) { c.ConnectGate = gate })
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)
	done := make(chan error, 1)
	go func() { done <- r.Connect(context.Background(), "s") }()

	waitForStatus(t, r, "s", StatusConnecting)
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("connect: %v", err)
	}
	if s, _ := r.Get("s"); s.Status != StatusConnected {
		t.Fatalf("status after connect = %q, want %q", s.Status, StatusConnected)
	}
}

func TestDisableDuringFailingConnect(t *testing.T) {
	gate := make(chan struct{})
	factory, _ := trackingFactory(func(c *MockClient) {
		c.ConnectGate = gate
		c.ConnectErr = fmt.Errorf("boom")
	})
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)
	done := make(chan error, 1)
	go func() { done <- r.Connect(context.Background(), "s") }()

	waitForStatus(t, r, "s", StatusConnecting)
	if err := r.Disable("s"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	close(gate)
	if err := <-done; err == nil {
		t.Fatal("connect should report failure")
	}
	s, _ := r.Get("s")
	if s.Status != StatusDisabled || s.LastErr != nil {
		t.Fatalf("disabled state overwritten: status=%q err=%v", s.Status, s.LastErr)
	}
}

func TestCloseCancelsInflightConnect(t *testing.T) {
	gate := make(chan struct{})
	factory, clients := trackingFactory(func(c *MockClient) { c.ConnectGate = gate })
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)
	done := make(chan error, 1)
	go func() { done <- r.Connect(context.Background(), "s") }()

	waitForStatus(t, r, "s", StatusConnecting)
	r.Close()
	if err := <-done; err == nil {
		t.Fatal("connect should fail once the registry is closed")
	}
	built := clients()
	if len(built) != 1 || !built[0].Closed() {
		t.Fatal("the in-flight client was not closed by shutdown")
	}
	s, _ := r.Get("s")
	if s.client != nil || s.Status == StatusConnecting {
		t.Fatalf("connect state survived Close: client=%v status=%q", s.client, s.Status)
	}
	if err := r.Connect(context.Background(), "s"); err == nil {
		t.Fatal("Connect after Close must fail")
	}
}

func TestDisableClosesOutsideLock(t *testing.T) {
	blockClose := make(chan struct{})
	factory, _ := trackingFactory(func(c *MockClient) { c.CloseGate = blockClose })
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)
	if err := r.Connect(context.Background(), "s"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = r.Disable("s"); close(done) }()
	got := make(chan struct{})
	go func() { _, _ = r.Get("s"); close(got) }()
	select {
	case <-got:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Registry.Get blocked while Disable was closing a client")
	}
	close(blockClose)
	<-done
}

func waitForStatus(t *testing.T, r *Registry, name string, want Status) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := r.Get(name); s.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	s, _ := r.Get(name)
	t.Fatalf("status never became %q (got %q)", want, s.Status)
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

func trackingFactory(configure func(*MockClient)) (ClientFactory, func() []*MockClient) {
	var mu sync.Mutex
	var built []*MockClient
	f := func(c ServerConfig) (Client, error) {
		mc := &MockClient{
			ServerName: c.Name,
			CannedTools: []Tool{{
				Server: c.Name,
				Name:   c.Name + "_echo",
			}},
		}
		if configure != nil {
			configure(mc)
		}
		mu.Lock()
		built = append(built, mc)
		mu.Unlock()
		return mc, nil
	}
	return f, func() []*MockClient {
		mu.Lock()
		defer mu.Unlock()
		return append([]*MockClient(nil), built...)
	}
}

// TestDoubleConnectRejected covers Bug 2: connecting an already-connected
// server must not silently overwrite (and thereby leak) the first client's
// subprocess. The second Connect should fail, exactly one client should ever
// be created, and that client must still be open (owned by the registry).
func TestDoubleConnectRejected(t *testing.T) {
	factory, clients := trackingFactory(nil)
	r := NewRegistry(sampleConfigs(), factory)

	if err := r.Connect(context.Background(), "files"); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if err := r.Connect(context.Background(), "files"); err == nil {
		t.Fatal("second Connect on an already-connected server succeeded, want error")
	}
	if got := len(clients()); got != 1 {
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
	factory, clients := trackingFactory(func(c *MockClient) { c.ConnectGate = release })
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
	open := 0
	for _, c := range clients() {
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
	factory, clients := trackingFactory(func(c *MockClient) { c.ConnectGate = gate })
	r := NewRegistry(sampleConfigs(), factory)

	connectErr := make(chan error, 1)
	go func() {
		connectErr <- r.Connect(context.Background(), "files")
	}()

	// Wait for the factory to have built its client (Connect is now
	// blocked inside client.Connect on the gate) before disabling.
	deadline := time.After(time.Second)
	for {
		ready := len(clients()) == 1
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

	built := clients()[0]
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
	factory, clients := trackingFactory(nil)
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

// A connect attempt can survive past a disable→enable→connect cycle: its
// in-flight response can win the race against its cancelled context. Such a
// stale attempt must not commit its client over the newer attempt's (that
// leaks the newer subprocess) and must not cancel the newer attempt's dial.
func TestStaleConnectCannotClobberNewerAttempt(t *testing.T) {
	gate := make(chan struct{})
	var gateMu sync.Mutex
	gateArmed := true
	factory, clients := trackingFactory(func(c *MockClient) {
		gateMu.Lock()
		if gateArmed {
			c.ListGate = gate
			gateArmed = false
		}
		gateMu.Unlock()
	})
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)
	done := make(chan error, 1)
	go func() { done <- r.Connect(context.Background(), "s") }()

	waitForStatus(t, r, "s", StatusConnecting)
	if err := r.Disable("s"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := r.Enable("s"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := r.Connect(context.Background(), "s"); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	close(gate)
	if err := <-done; err == nil {
		t.Fatal("stale connect attempt must be refused, not committed")
	}
	built := clients()
	if len(built) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(built))
	}
	if !built[0].Closed() {
		t.Fatal("stale attempt's client must be closed")
	}
	if built[1].Closed() {
		t.Fatal("the newer attempt's live client must not be closed")
	}
	s, _ := r.Get("s")
	if s.Status != StatusConnected || s.LastErr != nil {
		t.Fatalf("connected state clobbered by stale attempt: status=%q err=%v", s.Status, s.LastErr)
	}
}

// The stale attempt's failure path must be inert too: its error must not
// overwrite the Connected status the newer attempt committed.
func TestStaleConnectErrorPreservesNewerAttempt(t *testing.T) {
	gate := make(chan struct{})
	var gateMu sync.Mutex
	gateArmed := true
	factory, _ := trackingFactory(func(c *MockClient) {
		gateMu.Lock()
		if gateArmed {
			c.ListGate = gate
			c.ListErr = fmt.Errorf("boom")
			gateArmed = false
		}
		gateMu.Unlock()
	})
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)
	done := make(chan error, 1)
	go func() { done <- r.Connect(context.Background(), "s") }()

	waitForStatus(t, r, "s", StatusConnecting)
	if err := r.Disable("s"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := r.Enable("s"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := r.Connect(context.Background(), "s"); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	close(gate)
	if err := <-done; err == nil {
		t.Fatal("stale connect attempt should report its failure")
	}
	s, _ := r.Get("s")
	if s.Status != StatusConnected || s.LastErr != nil {
		t.Fatalf("stale error clobbered connected state: status=%q err=%v", s.Status, s.LastErr)
	}
}

func TestConnectRefusesInvalidServerName(t *testing.T) {
	factory := func(c ServerConfig) (Client, error) {
		t.Fatal("factory must not run for an invalid server name")
		return nil, nil
	}
	r := NewRegistry([]ServerConfig{{
		Name: "bad__name", Enabled: true, Transport: TransportStdio, Command: "srv",
	}}, factory)
	if err := r.Connect(context.Background(), "bad__name"); err == nil {
		t.Fatal("Connect accepted a server name containing the reserved __ separator")
	}
	if s, ok := r.Get("bad__name"); !ok || s.Status == StatusConnected {
		t.Errorf("server state after refused connect: %+v", s)
	}
}
