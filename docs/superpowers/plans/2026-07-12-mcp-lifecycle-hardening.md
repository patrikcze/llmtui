# MCP Lifecycle Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 9 verified code-review findings against commit `4a9d96e` ("fix(mcp): guard connect races and reap subprocess groups on every exit path") so MCP subprocess teardown is complete on every path, never blocks the UI, and the process-group helpers are shared with `run_command`.

**Architecture:** The `internal/mcp.Registry` becomes a real state machine (a `StatusConnecting` state replaces the ad-hoc `connecting bool`; all transitions go through two locked helpers) that is shutdown-aware (`Close` cancels and waits for in-flight connects). All blocking subprocess termination moves off the Bubble Tea `Update` goroutine (async `tea.Cmd`s + closing clients outside the registry mutex). The `Setpgid`/terminate helpers move to a new shared `internal/procutil` package used by both `internal/mcp` and `internal/tools.runCommand`.

**Tech Stack:** Go (module `github.com/patrikcze/llmtui`), Bubble Tea, `sync`, `os/exec`, `syscall` (platform-split with `//go:build` tags).

## Global Constraints

- Preserve every rule in the repo-root `CLAUDE.md`, especially "Workspace Tool Safety Invariants": any change touching `run_command` or the approval flow needs a regression test for the specific case it touches.
- Before any commit: `go fmt ./... && go vet ./... && go test ./...` must pass; also run `go test -race ./internal/mcp/... ./internal/procutil/...` after Tasks 1–4 and 6.
- Cross-platform: `GOOS=windows GOARCH=amd64 go build ./...` must stay green. `syscall` imports only in `//go:build !windows` / `windows` files.
- Keep the app buildable and runnable after every task. Commit at the end of every task.
- Match existing comment density and style (comments explain constraints, not narration).
- Do NOT weaken any existing test. All current tests must keep passing unmodified unless a task explicitly says to update one.

## Findings being fixed (for traceability)

| # | Finding | Task |
|---|---------|------|
| F1 | In-flight `/mcp connect` racing shutdown orphans a subprocess (`Registry.Close` can't see uncommitted clients; connect ctx detached from app lifetime) | 3 |
| F2 | SIGTERM path uses `p.Kill()` → no session auto-save, no exit summary, exit code 1 | 5 |
| F3 | `StdioClient.Close` blocks up to 2s/server on the Update goroutine (`quit()`, `/mcp disable|disconnect`) and under `r.mu` | 4 |
| F4 | A failing connect racing `Disable` stomps `StatusDisabled` via `setError`; `Enable` can't self-heal | 2 |
| F5 | `run_command` has the same orphaned-grandchild bug; helpers are private to `internal/mcp` | 6 |
| F6 | `connecting bool` is a second source of truth beside the `Status` enum | 1 |
| F7 | pgid-reuse TOCTOU in the SIGKILL sweep is undocumented | 6 |
| F8 | `Connect`'s four guards each hand-pair `Unlock()`+`return` — deadlock trap | 1 |
| F9 | Test scaffolding triplicated (client-tracking factories, poll-until-dead loops) | 6, 7 |

## File Structure

- `internal/mcp/registry.go` — state machine, shutdown-awareness, lock hygiene (Tasks 1–4)
- `internal/mcp/mock.go` — `MockClient.Connect` must honor ctx cancellation (Task 3)
- `internal/mcp/mcp_test.go` — new state/race tests + `trackingFactory` helper (Tasks 1–3, 7)
- `internal/mcp/stdio.go` — switch to `procutil` (Task 6)
- `internal/mcp/proc_unix.go`, `proc_windows.go`, `proc_unix_test.go` — DELETED, content moves to `internal/procutil` (Task 6)
- `internal/procutil/procutil.go`, `proc_unix.go`, `proc_windows.go`, `proc_unix_test.go` — NEW shared package (Task 6)
- `internal/tools/tools.go` (`runCommand` ~line 332) + `internal/tools/tools_test.go` — group-kill for run_command (Task 6)
- `internal/tui/app.go` — async quit, sigQuitMsg, `ErrProgramKilled` handling (Tasks 4, 5)
- `internal/tui/commands_local.go` — async `/mcp disable|disconnect` (Task 4)

---

### Task 1: Replace `connecting bool` with `StatusConnecting` and single-lock guard structure (F6, F8)

**Files:**
- Modify: `internal/mcp/registry.go` (Status consts ~line 15; `Server` struct ~line 24; `Connect` ~line 133)
- Test: `internal/mcp/mcp_test.go`

**Interfaces:**
- Consumes: existing `Registry`, `Server`, `Status`, `MockClient` (which already has a `ConnectGate chan struct{}` field that, when non-nil, makes `Connect` block until the gate channel is closed).
- Produces: `StatusConnecting Status = "connecting"`; unexported `func (r *Registry) beginConnect(name string) (*Server, ClientFactory, error)` — Tasks 2 and 3 build on both. The `connecting` field on `Server` is gone.

- [ ] **Step 1: Write the failing test**

Append to `internal/mcp/mcp_test.go` (reuse the file's existing imports; `ConnectGate` and `NewRegistry` usage should mirror the existing `TestConcurrentConnectOnlyOneWins`):

```go
// TestStatusConnectingDuringDial: while a connect is dialing, the server's
// public Status must be StatusConnecting (single source of truth — no side
// flag), and it must return to a terminal status afterwards.
func TestStatusConnectingDuringDial(t *testing.T) {
	gate := make(chan struct{})
	factory := func(c ServerConfig) (Client, error) {
		return &MockClient{ConnectGate: gate}, nil
	}
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)

	done := make(chan error, 1)
	go func() { done <- r.Connect(context.Background(), "s") }()

	// Wait for the dial to be in flight, then observe the public status.
	deadline := time.Now().Add(2 * time.Second)
	for {
		s, _ := r.Get("s")
		if s.Status == StatusConnecting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never became %q (got %q)", StatusConnecting, s.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("connect: %v", err)
	}
	if s, _ := r.Get("s"); s.Status != StatusConnected {
		t.Fatalf("status after connect = %q, want %q", s.Status, StatusConnected)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/mcp/ -run TestStatusConnectingDuringDial -v`
Expected: FAIL (`undefined: StatusConnecting`).

- [ ] **Step 3: Implement**

In `internal/mcp/registry.go`:

(a) Add the status value to the const block:

```go
	StatusConnecting  Status = "connecting"   // dial/handshake in flight
```

(b) Delete the `connecting bool` field from `Server` (keep `client Client`).

(c) Replace the guard section of `Connect` (currently four hand-paired `Unlock(); return` branches plus `s.connecting = true`) with one locked helper. `beginConnect` acquires the lock once, validates everything, marks the server `StatusConnecting`, and returns; there is exactly one unlock:

```go
// beginConnect validates that a connect may start and, if so, transitions the
// server to StatusConnecting. All precondition checks live behind one lock
// acquisition so a future guard cannot forget its paired unlock.
func (r *Registry) beginConnect(name string) (*Server, ClientFactory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.serverLocked(name)
	if !ok {
		return nil, nil, fmt.Errorf("no MCP server named %q", name)
	}
	switch {
	case s.client != nil:
		return nil, nil, fmt.Errorf("MCP server %q is already connected", name)
	case s.Status == StatusConnecting:
		return nil, nil, fmt.Errorf("MCP server %q: connect already in progress", name)
	case !s.Config.Enabled:
		return nil, nil, fmt.Errorf("MCP server %q is disabled", name)
	}
	s.Status = StatusConnecting
	return s, r.factory, nil
}
```

(d) `Connect` becomes:

```go
func (r *Registry) Connect(ctx context.Context, name string) error {
	s, factory, err := r.beginConnect(name)
	if err != nil {
		return err
	}
	if factory == nil {
		r.setError(s, StatusNoTransport, fmt.Errorf("no MCP transport is available in this build"))
		return s.LastErr
	}
	client, err := factory(s.Config)
	if err != nil {
		r.setError(s, StatusError, err)
		return err
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		r.setError(s, StatusError, err)
		return err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		r.setError(s, StatusError, err)
		return err
	}

	r.mu.Lock()
	if !s.Config.Enabled {
		// Disabled while we were dialing/handshaking: don't resurrect the
		// server as connected out from under Disable.
		r.mu.Unlock()
		_ = client.Close()
		return fmt.Errorf("MCP server %q was disabled during connect", name)
	}
	s.client = client
	s.Tools = tools
	s.Status = StatusConnected
	s.LastErr = nil
	r.mu.Unlock()
	return nil
}
```

Note: the previous "belt and braces `if s.client != nil { Close }`" block at the commit point is deleted — `beginConnect`'s `StatusConnecting` exclusion makes it unreachable, and Task 3 adds the shutdown re-check here.

(e) `Disable` must also clear an in-flight marker sanely: it already sets `Status = StatusDisabled`; no change needed here (the commit path re-checks `Enabled`). But `setError` must not resurrect a `StatusConnecting` snapshot label — leave `setError` as-is for now; Task 2 rewrites it.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/mcp/ -v` then `go test -race ./internal/mcp/`
Expected: all PASS, including the pre-existing `TestConcurrentConnectOnlyOneWins` and double-connect tests (their asserted error strings — "already connected", "connect already in progress" — are preserved above; if a pre-existing test asserted on the exact old wording, keep the wording identical rather than changing the test).

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/registry.go internal/mcp/mcp_test.go
git commit -m "refactor(mcp): make StatusConnecting the single source of connect state"
```

---

### Task 2: A failing connect must not stomp a concurrent Disable (F4)

**Files:**
- Modify: `internal/mcp/registry.go` (`setError` ~line 212)
- Test: `internal/mcp/mcp_test.go`

**Interfaces:**
- Consumes: `beginConnect`/`StatusConnecting` from Task 1; `MockClient.ConnectGate`; `MockClient.ConnectErr` — check `internal/mcp/mock.go`: if `MockClient` has no way to fail `Connect` after the gate opens, add a `ConnectErr error` field returned by `Connect` after the gate wait.
- Produces: `setError` re-checks `Config.Enabled` under the lock. No signature change.

- [ ] **Step 1: Write the failing test**

```go
// TestDisableDuringFailingConnect: the success path already refuses to
// resurrect a server disabled mid-connect; the ERROR path must not stomp
// StatusDisabled either (Enable's self-heal only fires from StatusDisabled).
func TestDisableDuringFailingConnect(t *testing.T) {
	gate := make(chan struct{})
	factory := func(c ServerConfig) (Client, error) {
		return &MockClient{ConnectGate: gate, ConnectErr: fmt.Errorf("boom")}, nil
	}
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)

	done := make(chan error, 1)
	go func() { done <- r.Connect(context.Background(), "s") }()

	// Wait until the dial is in flight, then disable.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if s, _ := r.Get("s"); s.Status == StatusConnecting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connect never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := r.Disable("s"); err != nil {
		t.Fatalf("disable: %v", err)
	}

	close(gate) // the mock's Connect now returns ConnectErr
	if err := <-done; err == nil {
		t.Fatal("connect should report failure")
	}
	s, _ := r.Get("s")
	if s.Status != StatusDisabled {
		t.Fatalf("status = %q, want %q (setError stomped Disable)", s.Status, StatusDisabled)
	}
	if s.LastErr != nil {
		t.Fatalf("LastErr = %v, want nil on a disabled server", s.LastErr)
	}
}
```

If `MockClient` lacks `ConnectErr`, add it in `internal/mcp/mock.go`: a `ConnectErr error` field; `Connect` waits on `ConnectGate` (existing behavior) and then returns `ConnectErr` if non-nil.

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/mcp/ -run TestDisableDuringFailingConnect -v`
Expected: FAIL — status is `"error"`, want `"disabled"`.

- [ ] **Step 3: Implement**

Replace `setError` in `internal/mcp/registry.go`:

```go
// setError records a failed transition. If the server was disabled while the
// attempt was in flight, Disable's outcome wins: a disabled server must not
// resurface as errored (Enable only self-heals from StatusDisabled).
func (r *Registry) setError(s *Server, status Status, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !s.Config.Enabled {
		s.Status = StatusDisabled
		s.LastErr = nil
		return
	}
	s.Status = status
	s.LastErr = err
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/mcp/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/registry.go internal/mcp/mock.go internal/mcp/mcp_test.go
git commit -m "fix(mcp): keep a mid-connect Disable authoritative when the connect fails"
```

---

### Task 3: Shutdown-aware registry — Close cancels and awaits in-flight connects (F1)

**Files:**
- Modify: `internal/mcp/registry.go` (`Registry` struct, `beginConnect`, `Connect`, `Close`)
- Modify: `internal/mcp/mock.go` (MockClient.Connect must honor ctx)
- Test: `internal/mcp/mcp_test.go`

**Interfaces:**
- Consumes: Task 1's `beginConnect`, Task 2's `setError`.
- Produces: `Registry.Close()` now (a) refuses future connects, (b) cancels in-flight ones, (c) blocks until they have fully unwound (subprocess reaped). `Connect` returns an error if the registry closed mid-flight. `MockClient.Connect(ctx)` returns `ctx.Err()` if the context is done while gated.

- [ ] **Step 1: Write the failing test**

```go
// TestCloseCancelsInflightConnect: quitting while /mcp connect is dialing must
// not orphan the subprocess. Close cancels the in-flight connect, waits for it
// to unwind, and the fresh client must be closed — never committed.
func TestCloseCancelsInflightConnect(t *testing.T) {
	gate := make(chan struct{}) // never closed: only ctx cancellation releases it
	var built *MockClient
	factory := func(c ServerConfig) (Client, error) {
		built = &MockClient{ConnectGate: gate}
		return built, nil
	}
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)

	done := make(chan error, 1)
	go func() { done <- r.Connect(context.Background(), "s") }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if s, _ := r.Get("s"); s.Status == StatusConnecting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connect never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	r.Close() // must return only after the in-flight connect has unwound

	if err := <-done; err == nil {
		t.Fatal("connect should fail once the registry is closed")
	}
	if built == nil || !built.Closed() {
		t.Fatal("the in-flight client was not closed by shutdown")
	}
	if s, _ := r.Get("s"); s.client != nil {
		t.Fatal("client committed after Close")
	}
	// And no new connects after Close:
	if err := r.Connect(context.Background(), "s"); err == nil {
		t.Fatal("Connect after Close must fail")
	}
}
```

(If `MockClient` has no `Closed() bool` accessor, check `mock.go` — one exists in the current tests' usage; if it is a plain field, adapt the assertion to whatever the existing double-connect test uses.)

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/mcp/ -run TestCloseCancelsInflightConnect -v`
Expected: FAIL — either it hangs on `<-done` (gate never released; add `-timeout 30s`) or `Close` returns while the connect is still in flight.

- [ ] **Step 3: Implement**

(a) `internal/mcp/mock.go` — `MockClient.Connect` must select on the ctx while gated (keep existing mutex/`connected` bookkeeping around it):

```go
func (m *MockClient) Connect(ctx context.Context) error {
	if m.ConnectGate != nil {
		select {
		case <-m.ConnectGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m.ConnectErr != nil {
		return m.ConnectErr
	}
	// ... existing "mark connected" bookkeeping unchanged ...
	return nil
}
```

(b) `internal/mcp/registry.go` — add shutdown state:

```go
type Registry struct {
	mu       sync.Mutex
	factory  ClientFactory
	servers  map[string]*Server
	order    []string
	closed   bool
	inflight sync.WaitGroup
}
```

Add a per-server cancel slot on `Server`:

```go
	client        Client
	connectCancel context.CancelFunc // set while a Connect is dialing; guarded by Registry.mu
```

(c) `beginConnect` grows a ctx parameter and registers the flight (still one lock, one unlock):

```go
func (r *Registry) beginConnect(ctx context.Context, name string) (context.Context, *Server, ClientFactory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, nil, fmt.Errorf("mcp: registry is closed")
	}
	s, ok := r.serverLocked(name)
	if !ok {
		return nil, nil, nil, fmt.Errorf("no MCP server named %q", name)
	}
	switch {
	case s.client != nil:
		return nil, nil, nil, fmt.Errorf("MCP server %q is already connected", name)
	case s.Status == StatusConnecting:
		return nil, nil, nil, fmt.Errorf("MCP server %q: connect already in progress", name)
	case !s.Config.Enabled:
		return nil, nil, nil, fmt.Errorf("MCP server %q is disabled", name)
	}
	ctx, cancel := context.WithCancel(ctx)
	s.Status = StatusConnecting
	s.connectCancel = cancel
	r.inflight.Add(1)
	return ctx, s, r.factory, nil
}
```

(d) `Connect` wraps the whole flight with the unwind bookkeeping and re-checks `closed` at commit:

```go
func (r *Registry) Connect(ctx context.Context, name string) error {
	ctx, s, factory, err := r.beginConnect(ctx, name)
	if err != nil {
		return err
	}
	defer func() {
		r.mu.Lock()
		if s.connectCancel != nil {
			s.connectCancel()
			s.connectCancel = nil
		}
		r.mu.Unlock()
		r.inflight.Done()
	}()

	if factory == nil {
		r.setError(s, StatusNoTransport, fmt.Errorf("no MCP transport is available in this build"))
		return s.LastErr
	}
	client, err := factory(s.Config)
	if err != nil {
		r.setError(s, StatusError, err)
		return err
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		r.setError(s, StatusError, err)
		return err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		r.setError(s, StatusError, err)
		return err
	}

	r.mu.Lock()
	if r.closed || !s.Config.Enabled {
		// Refused commit: never leave the server stuck in StatusConnecting.
		reason := "was disabled during connect"
		if r.closed {
			reason = "registry closed during connect"
			s.Status = StatusConfigured
		} else {
			s.Status = StatusDisabled
		}
		r.mu.Unlock()
		_ = client.Close()
		return fmt.Errorf("MCP server %q %s", name, reason)
	}
	s.client = client
	s.Tools = tools
	s.Status = StatusConnected
	s.LastErr = nil
	r.mu.Unlock()
	return nil
}
```

(Note `setError` from Task 2 already resets a disabled server to `StatusDisabled`, so every error branch above also leaves `StatusConnecting`. Extend the Step-1 test with a final assertion that the server's status is not `StatusConnecting` after Close.)

(e) `Close` cancels flights, closes committed clients outside the lock, and waits:

```go
// Close refuses future connects, cancels in-flight ones, tears down every
// open connection, and returns only when all of that has unwound. Safe on a
// nil registry and safe to call more than once.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	var clients []Client
	for _, s := range r.servers {
		if s.connectCancel != nil {
			s.connectCancel() // in-flight Connect unwinds via its ctx
		}
		if s.client != nil {
			clients = append(clients, s.client)
			s.client = nil
		}
	}
	r.mu.Unlock()
	for _, c := range clients {
		_ = c.Close() // outside r.mu: Close can block ~2s per process
	}
	r.inflight.Wait()
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race -timeout 60s ./internal/mcp/ -v` and re-run the concurrency tests hard: `go test -race -count=10 -run "Concurrent|Disable|Close" ./internal/mcp/`
Expected: all PASS, no hangs. NOTE: pre-existing tests that call `Close` then `Connect` again (if any) will now fail with "registry is closed" — that is the intended new contract; update such a test's expectation only if it exists and only in that direction.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/registry.go internal/mcp/mock.go internal/mcp/mcp_test.go
git commit -m "fix(mcp): Close cancels and awaits in-flight connects so no subprocess escapes shutdown"
```

---

### Task 4: Never block the Update goroutine — async quit and async disable/disconnect (F3)

**Files:**
- Modify: `internal/mcp/registry.go` (`Disable` ~line 115)
- Modify: `internal/tui/app.go` (`quit()` ~line 1129; `Update`'s message switch near `mcpConnectMsg` at ~line 595; `Model` struct)
- Modify: `internal/tui/commands_local.go` (`cmdMcp` "disable" ~line 1340 and "disconnect" ~line 1376; message types near `mcpConnectMsg` ~line 489)
- Test: `internal/mcp/mcp_test.go`, `internal/tui/` (follow the existing update-logic test style in `internal/tui/*_test.go`)

**Interfaces:**
- Consumes: Task 3's `Registry.Close` (already closes clients outside `r.mu`).
- Produces: `Registry.Disable` closes the client outside `r.mu`. New TUI messages `mcpDisconnectMsg{server string, err error}` and `quitDoneMsg struct{}`; new `Model` field `quitting bool`. `m.quit()` now returns a `tea.Cmd` that performs save+close off-thread and sends `quitDoneMsg`; `Update` returns `tea.Quit` on `quitDoneMsg`. Task 5 reuses this exact quit flow.

- [ ] **Step 1: Write the failing registry test**

```go
// TestDisableClosesOutsideLock: Disable must not hold the registry mutex while
// the client's (potentially 2s-blocking) Close runs — concurrent registry
// reads must proceed.
func TestDisableClosesOutsideLock(t *testing.T) {
	blockClose := make(chan struct{})
	c := &MockClient{CloseGate: blockClose} // add CloseGate to MockClient: Close blocks until the gate closes
	factory := func(ServerConfig) (Client, error) { return c, nil }
	r := NewRegistry([]ServerConfig{{Name: "s", Enabled: true, Transport: TransportStdio, Command: "x"}}, factory)
	if err := r.Connect(context.Background(), "s"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	done := make(chan struct{})
	go func() { _ = r.Disable("s"); close(done) }()

	// While Disable is blocked in client.Close, a read must still succeed fast.
	got := make(chan struct{})
	go func() { r.Get("s"); close(got) }()
	select {
	case <-got:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Registry.Get blocked while Disable was closing a client — mutex held across Close")
	}
	close(blockClose)
	<-done
}
```

Add `CloseGate chan struct{}` to `MockClient` in `mock.go`: if non-nil, `Close` blocks on it before the existing bookkeeping (nil-safe, only tests set it).

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/mcp/ -run TestDisableClosesOutsideLock -v -timeout 30s`
Expected: FAIL at "Registry.Get blocked…".

- [ ] **Step 3: Implement the registry half**

Rewrite `Disable` in `internal/mcp/registry.go`:

```go
// Disable closes any connection and marks the server disabled. The client is
// closed outside the registry lock: Close can block for the SIGTERM grace
// period and must not stall unrelated registry access.
func (r *Registry) Disable(name string) error {
	r.mu.Lock()
	s, ok := r.serverLocked(name)
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("no MCP server named %q", name)
	}
	client := s.client
	s.client = nil
	s.Config.Enabled = false
	s.Status = StatusDisabled
	s.Tools = nil
	s.LastErr = nil
	if s.connectCancel != nil {
		s.connectCancel()
	}
	r.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	return nil
}
```

Run: `go test -race ./internal/mcp/ -v` — all PASS.

- [ ] **Step 4: Implement the TUI half**

(a) `internal/tui/commands_local.go` — next to `mcpConnectMsg` (~line 489) add:

```go
// mcpDisconnectMsg reports the outcome of an async /mcp disable|disconnect.
type mcpDisconnectMsg struct {
	server   string
	reenable bool // disconnect keeps the server enabled for a later connect
	err      error
}
```

(b) Replace the synchronous bodies of the `"disable"` and `"disconnect"` cases in `cmdMcp` (mirroring the async `"connect"` case directly above them):

```go
	case "disable":
		name := strings.TrimSpace(rest)
		if name == "" {
			return m.fail("usage: /mcp disable <server>")
		}
		reg := m.mcpRegistry
		m.notice = fmt.Sprintf("🔌 disabling MCP server %q…", name)
		return func() tea.Msg {
			return mcpDisconnectMsg{server: name, err: reg.Disable(name)}
		}
	case "disconnect":
		name := strings.TrimSpace(rest)
		if name == "" {
			return m.fail("usage: /mcp disconnect <server>")
		}
		reg := m.mcpRegistry
		m.notice = fmt.Sprintf("🔌 disconnecting MCP server %q…", name)
		return func() tea.Msg {
			err := reg.Disable(name)
			if err == nil {
				err = reg.Enable(name) // stay available for a later /mcp connect
			}
			return mcpDisconnectMsg{server: name, reenable: true, err: err}
		}
```

(c) `internal/tui/app.go` — in `Update`'s switch, next to `case mcpConnectMsg:` (~line 595) add:

```go
	case mcpDisconnectMsg:
		m.notice = ""
		if msg.err != nil {
			m.errText = fmt.Sprintf("MCP %q: %s", msg.server, msg.err.Error())
			m.refreshViewport()
		} else if msg.reenable {
			m.notice = fmt.Sprintf("🔌 MCP server %q disconnected", msg.server)
		} else {
			m.notice = fmt.Sprintf("🔌 MCP server %q disabled", msg.server)
		}
		return m, nil
```

(d) `internal/tui/app.go` — make `quit()` async. Add to `Model`: `quitting bool`. Add message type near the other msg types in app.go: `type quitDoneMsg struct{}`. Rewrite `quit()`:

```go
// quit stops any stream and hands shutdown to a background command: the
// session save and MCP teardown can each block (disk, SIGTERM grace), so they
// must not run on the Update goroutine. quitDoneMsg then exits the program.
func (m *Model) quit() tea.Cmd {
	if m.quitting {
		return nil
	}
	m.quitting = true
	if m.cancelStream != nil {
		m.cancelStream()
	}
	m.notice = "shutting down…"
	reg := m.mcpRegistry
	saveNeeded := m.historyDir != "" && m.hasUserContent()
	return func() tea.Msg {
		if saveNeeded {
			if path, err := m.saveSession(); err == nil {
				m.savedPath = path
			}
		}
		reg.Close() // nil-safe; stops MCP subprocesses
		return quitDoneMsg{}
	}
}
```

CAREFUL: `m.saveSession()` and `m.savedPath` are now touched off the Update goroutine. Audit `saveSession` — if it reads mutable Model state (messages), snapshot what it needs before returning the closure, or keep the save synchronous (it's fast disk I/O; only `reg.Close()` is the slow part) and move only `reg.Close()` into the command:

```go
	if saveNeeded {
		if path, err := m.saveSession(); err == nil {
			m.savedPath = path
		}
	}
	reg := m.mcpRegistry
	return func() tea.Msg { reg.Close(); return quitDoneMsg{} }
```

Prefer this second form — it keeps all Model access on the Update goroutine. In `Update`, add:

```go
	case quitDoneMsg:
		return m, tea.Quit
```

- [ ] **Step 5: Run everything**

Run: `go test -race ./internal/mcp/... ./internal/tui/...` then `go vet ./...` and build: `go build ./...`
Expected: PASS. Manually sanity-check: `go run ./cmd/llmtui chat`, double-Ctrl+C exits promptly.

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/ internal/tui/
git commit -m "fix(tui,mcp): move blocking MCP teardown off the Update goroutine"
```

---

### Task 5: Graceful SIGTERM/SIGHUP — auto-save, exit summary, exit code 0 (F2)

**Files:**
- Modify: `internal/tui/app.go` (`Run` ~line 1848-1895; `Update` switch)
- Test: `internal/tui/` update-logic test

**Interfaces:**
- Consumes: Task 4's async `quit()` and `quitDoneMsg`.
- Produces: `type sigQuitMsg struct{}` handled in `Update` by calling `m.quit()`. `Run` treats `tea.ErrProgramKilled` as a handled outcome (exit 0, no summary) instead of an error.

- [ ] **Step 1: Write the failing test**

In the existing TUI test file that exercises `Update` (find with `grep -rn "func Test.*Update" internal/tui/*_test.go` and follow its Model-construction helper):

```go
// A sigQuitMsg must route through the same graceful quit as Ctrl+C: the model
// marks itself quitting and returns the shutdown command (session save + MCP
// teardown), not an immediate tea.Quit.
func TestSigQuitMsgTriggersGracefulQuit(t *testing.T) {
	m := newTestModel(t) // use the file's existing constructor helper
	_, cmd := m.Update(sigQuitMsg{})
	if !m.quitting {
		t.Fatal("sigQuitMsg did not start the quit flow")
	}
	if cmd == nil {
		t.Fatal("sigQuitMsg must return the shutdown command")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/tui/ -run TestSigQuitMsgTriggersGracefulQuit -v`
Expected: FAIL (`undefined: sigQuitMsg`).

- [ ] **Step 3: Implement**

(a) In `internal/tui/app.go` define `type sigQuitMsg struct{}` next to `quitDoneMsg`, and handle it in `Update`:

```go
	case sigQuitMsg:
		return m, m.quit()
```

(b) Rewrite the signal block in `Run` (currently: `sigCh` → `m.mcpRegistry.Close(); p.Kill()`):

```go
	// SIGTERM/SIGHUP (terminal closing, a process manager stopping us) should
	// get the same graceful path as Ctrl+C: save the session, stop MCP
	// subprocesses, print the exit summary, exit 0. A second signal (or a
	// wedged shutdown) escalates to a hard kill after a grace period.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigCh:
			p.Send(sigQuitMsg{})
		case <-done:
			return
		}
		select {
		case <-sigCh: // second signal: the user means it
		case <-time.After(10 * time.Second): // graceful path wedged
		case <-done:
			return
		}
		m.mcpRegistry.Close()
		p.Kill()
	}()
	defer signal.Stop(sigCh)

	final, err := p.Run()
	if err != nil {
		if errors.Is(err, tea.ErrProgramKilled) {
			return nil // hard-killed by the escalation path; teardown already ran
		}
		return fmt.Errorf("run TUI: %w", err)
	}
```

(`errors` and `time` are already imported in app.go; verify and add if not.)

- [ ] **Step 4: Run tests + manual verification**

Run: `go test -race ./internal/tui/...` — PASS.
Manual: build (`go build -o /tmp/llmtui ./cmd/llmtui`), run `/tmp/llmtui chat` in one terminal, `kill <pid>` from another. Expected: app exits, exit summary printed, `echo $?` → 0, and any connected MCP server process is gone (`ps aux | grep <server>`).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/
git commit -m "fix(tui): route SIGTERM/SIGHUP through graceful quit (auto-save, summary, exit 0)"
```

---

### Task 6: Shared `internal/procutil` + group-kill for `run_command` (F5, F7, part of F9)

**Files:**
- Create: `internal/procutil/procutil.go`, `internal/procutil/proc_unix.go`, `internal/procutil/proc_windows.go`, `internal/procutil/proc_unix_test.go`
- Delete: `internal/mcp/proc_unix.go`, `internal/mcp/proc_windows.go`, `internal/mcp/proc_unix_test.go`
- Modify: `internal/mcp/stdio.go` (~line 113 `setupProcAttr` call, ~line 240 `terminateProcess` call)
- Modify: `internal/tools/tools.go` (`runCommand`, lines ~332-372)
- Test: `internal/tools/tools_test.go` (or `guardrails_test.go` — put it beside the existing run_command tests)

**Interfaces:**
- Consumes: the existing `internal/mcp/proc_unix.go` implementations (move, don't rewrite).
- Produces: `procutil.SetupProcAttr(cmd *exec.Cmd)`, `procutil.Terminate(cmd *exec.Cmd)` (TERM group → 2s grace → KILL group → reap; used by mcp), `procutil.KillGroup(cmd *exec.Cmd)` (immediate best-effort group SIGKILL sweep, no wait; used by run_command after a timeout), and test helper `waitUntilDead` stays package-private to procutil's tests.

- [ ] **Step 1: Move the package**

`internal/procutil/procutil.go`:

```go
// Package procutil manages subprocess groups: children started via wrapper
// commands (npx, uvx, sh -c) put their real work in grandchildren, which a
// plain Process.Kill would orphan. On Unix the helpers place the child in its
// own process group and signal the whole group; on Windows they degrade to
// direct-child termination (Job Objects are future work).
package procutil
```

`internal/procutil/proc_unix.go` — the current `internal/mcp/proc_unix.go` content with: exported names (`SetupProcAttr`, `Terminate`), a new `KillGroup`, and the F7 comment on the sweep:

```go
//go:build !windows

package procutil

import (
	"os/exec"
	"syscall"
	"time"
)

// SetupProcAttr puts the subprocess in its own process group so Terminate/
// KillGroup can signal the whole group (the command plus any wrapper
// grandchildren it spawns, e.g. npx/uvx/sh -c) instead of just the child.
func SetupProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Terminate asks the process group to exit (SIGTERM), gives it a grace
// period, then forces it (SIGKILL) and reaps the direct child.
//
// Signaling -pid after the leader is reaped is a known, accepted TOCTOU: the
// kernel could in principle recycle the pgid for an unrelated group in that
// window. Every group-kill implementation without pidfd/cgroup support
// carries this; the window is microseconds and the alternative (leaving the
// group running) is strictly worse.
func Terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	_ = syscall.Kill(-pid, syscall.SIGTERM)

	select {
	case <-done:
		// The direct child is reaped, but a grandchild that ignores SIGTERM
		// (with its wrapper already gone) would survive: sweep the group.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return
	case <-time.After(2 * time.Second):
	}

	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-done
}

// KillGroup force-kills the process group without waiting. For callers (like
// run_command timeouts) where os/exec has already reaped the direct child and
// only stray grandchildren may remain. See Terminate for the pgid-reuse note.
func KillGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
```

`internal/procutil/proc_windows.go` — current `internal/mcp/proc_windows.go` content with exported names plus `func KillGroup(cmd *exec.Cmd) {}` (no-op, same Job-Object future-work comment).

`internal/procutil/proc_unix_test.go` — move the three tests from `internal/mcp/proc_unix_test.go`, renaming `terminateProcess`→`Terminate`, `setupProcAttr`→`SetupProcAttr`, and extract the repeated poll loop (F9):

```go
// waitUntilDead fails the test if pid is still alive after timeout.
func waitUntilDead(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %s", pid, timeout)
}
```

NOTE: `TestTerminateProcessIsIdempotentViaClose` in the old file exercises `StdioClient` (package mcp) — that one stays in `internal/mcp` (move it into `mcp_test.go` or a new `stdio_close_test.go` with its own tiny alive/poll copy or an exported-for-test seam; simplest: keep it in package mcp with a local `waitUntilDead`).

(b) `internal/mcp/stdio.go`: import `github.com/patrikcze/llmtui/internal/procutil`; replace `setupProcAttr(cmd)` → `procutil.SetupProcAttr(cmd)` and `terminateProcess(c.cmd)` → `procutil.Terminate(c.cmd)`. Delete the three old proc files.

Run: `go test -race ./internal/mcp/... ./internal/procutil/...` and `GOOS=windows GOARCH=amd64 go build ./...` — PASS before proceeding.

- [ ] **Step 2: Write the failing run_command test**

Beside the existing run_command tests (find them: `grep -rn "runCommand\|run_command" internal/tools/*_test.go` and reuse that file's Runner construction helper — CLAUDE.md requires a regression test for any run_command change):

```go
// A timed-out run_command must not orphan backgrounded grandchildren: the
// same wrapper-process bug fixed for MCP servers applies to `sh -c`.
func TestRunCommandTimeoutReapsGrandchild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix-only")
	}
	r := newTestRunner(t) // the file's existing constructor; set r.CommandTimeout = time.Second
	r.CommandTimeout = time.Second

	out, err := r.runCommand(`sleep 30 & echo $!; wait`)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(strings.Split(out, "\n")[0]))
	if perr != nil {
		t.Fatalf("no grandchild pid in output %q: %v", out, perr)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // grandchild is gone
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived the run_command timeout", pid)
}
```

(Adapt `newTestRunner`/field names to the file's existing helpers; the test must construct the Runner the same way its neighbors do. If `runCommand` is unexported and tests are in package `tools`, call it directly as above.)

- [ ] **Step 3: Run it to make sure it fails**

Run: `go test ./internal/tools/ -run TestRunCommandTimeoutReapsGrandchild -v -timeout 60s`
Expected: FAIL — grandchild survives. (It may ALSO hang before failing: with no `WaitDelay`, `CombinedOutput` blocks on the pipe the grandchild holds even after `sh` is killed. Both symptoms confirm the bug.)

- [ ] **Step 4: Implement**

In `internal/tools/tools.go` `runCommand`, after `cmd.Env = sanitizedEnv(os.Environ())` (~line 355) and before `CombinedOutput`:

```go
	// The command runs in its own process group so a timeout can reap
	// grandchildren (backgrounded jobs, wrapper commands) — see procutil.
	procutil.SetupProcAttr(cmd)
	// Without WaitDelay, a grandchild holding the output pipe would block
	// CombinedOutput past the timeout even after sh itself is killed.
	cmd.WaitDelay = time.Second
```

and after the `CombinedOutput` call, sweep on timeout (inside the existing `DeadlineExceeded` branch at ~line 362):

```go
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		procutil.KillGroup(cmd)
		return output, fmt.Errorf("command timed out after %s", timeout)
	}
```

Add the import `"github.com/patrikcze/llmtui/internal/procutil"`.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/tools/ -v -timeout 120s` and the full suite `go test ./...`; `GOOS=windows GOARCH=amd64 go build ./...`
Expected: all PASS, including every pre-existing guardrails test (run_command classification/confinement must be untouched — this change only adds group attributes and a sweep).

- [ ] **Step 6: Commit**

```bash
git add internal/procutil/ internal/mcp/ internal/tools/
git rm internal/mcp/proc_unix.go internal/mcp/proc_windows.go internal/mcp/proc_unix_test.go 2>/dev/null || true
git commit -m "refactor(procutil): share process-group termination; reap run_command grandchildren on timeout"
```

---

### Task 7: Deduplicate the client-tracking test factories (rest of F9)

**Files:**
- Modify: `internal/mcp/mcp_test.go` (`countingFactory` ~line 194; inline factories in `TestConcurrentConnectOnlyOneWins` ~line 258 and `TestDisableDuringConnect` ~line 321)

**Interfaces:**
- Consumes: `MockClient` (with `ConnectGate`/`ConnectErr`/`CloseGate` from earlier tasks).
- Produces: one helper used by all client-tracking tests:

- [ ] **Step 1: Add the helper and refactor**

```go
// trackingFactory returns a ClientFactory that builds one MockClient per call
// (customized by configure, which may be nil) and a snapshot accessor for the
// clients built so far. Replaces the hand-rolled counting/capturing factories.
func trackingFactory(configure func(*MockClient)) (ClientFactory, func() []*MockClient) {
	var mu sync.Mutex
	var built []*MockClient
	factory := func(c ServerConfig) (Client, error) {
		m := &MockClient{CannedTools: []Tool{{Server: c.Name, Name: c.Name + "_echo"}}}
		if configure != nil {
			configure(m)
		}
		mu.Lock()
		built = append(built, m)
		mu.Unlock()
		return m, nil
	}
	snapshot := func() []*MockClient {
		mu.Lock()
		defer mu.Unlock()
		return append([]*MockClient(nil), built...)
	}
	return factory, snapshot
}
```

Refactor `countingFactory` callers, the inline factory in `TestConcurrentConnectOnlyOneWins`, and the single-client capture in `TestDisableDuringConnect` (and the Task 2/3 tests added by this plan) to use it. Delete the now-unused originals. Counting = `len(snapshot())`.

- [ ] **Step 2: Run the tests**

Run: `go test -race -count=5 ./internal/mcp/`
Expected: PASS, no flakes.

- [ ] **Step 3: Commit**

```bash
git add internal/mcp/mcp_test.go
git commit -m "test(mcp): one trackingFactory helper for client-capturing tests"
```

---

## Final verification (after all tasks)

- [ ] `go fmt ./... && go vet ./... && go test ./... && go test -race ./internal/mcp/... ./internal/procutil/... ./internal/tools/... ./internal/tui/...` — all clean.
- [ ] `GOOS=windows GOARCH=amd64 go build ./...` — clean.
- [ ] Manual smoke (Unix): build, `llmtui chat`, `/mcp connect <server>`, then: (a) double-Ctrl+C → prompt exit, no `<server>` process left (`ps aux | grep <server>`); (b) repeat but `kill <llmtui-pid>` instead → exit summary printed, `echo $?` = 0, no server process left; (c) `/mcp connect` a server, immediately double-Ctrl+C *while it is still connecting* → no server process left (this is F1, the headline fix).
- [ ] The findings table at the top: confirm each row's behavior is covered by a test or the manual step above.
