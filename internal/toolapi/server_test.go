package toolapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/testutil"
)

// testClient disables keep-alives so each request opens and closes its own
// connection deterministically. Reusing http.DefaultClient here let the
// Transport race a fresh dial against reusing the idle keep-alive
// connection; the losing connection could still be sitting in the server's
// non-idle "new" state when t.Cleanup called Shutdown, blocking it until
// the context deadline (see server_test.go history for the flake).
var testClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func startTestServer(t *testing.T, opts Options) *Server {
	t.Helper()
	server, err := Start(opts)
	if err != nil {
		testutil.SkipIfListenerUnavailable(t, err)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown tool API: %v", err)
		}
	})
	return server
}

func TestServerReturnsDynamicToolSnapshots(t *testing.T) {
	calls := 0
	server := startTestServer(t, Options{
		Listen: "127.0.0.1:0",
		Source: func(context.Context) ([]Tool, error) {
			calls++
			tools := []Tool{{Name: "read_file", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			if calls > 1 {
				tools = append(tools, Tool{Name: "mcp__jira__issue_search", Source: "mcp:jira", InputSchema: json.RawMessage(`{"type":"object"}`)})
			}
			return tools, nil
		},
	})

	first := getSnapshot(t, server, "")
	second := getSnapshot(t, server, "")
	if len(first.Tools) != 1 || len(second.Tools) != 2 || second.Tools[1].Name != "mcp__jira__issue_search" {
		t.Fatalf("snapshots did not refresh: first=%+v second=%+v", first.Tools, second.Tools)
	}
	if first.APIVersion != APIVersion || first.Generated.IsZero() {
		t.Fatalf("snapshot metadata = %+v", first)
	}
}

func TestServerRequiresConfiguredBearerToken(t *testing.T) {
	t.Setenv("LLMTUI_REGISTRY_TOKEN", "secret-token")
	server := startTestServer(t, Options{
		Listen:   "127.0.0.1:0",
		TokenEnv: "LLMTUI_REGISTRY_TOKEN",
		Source:   func(context.Context) ([]Tool, error) { return nil, nil },
	})

	resp, err := testClient.Get("http://" + server.Addr() + ToolsPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}

	snapshot := getSnapshot(t, server, "secret-token")
	if snapshot.Tools == nil {
		t.Fatal("empty catalog must be encoded as [] rather than null")
	}
}

func TestServerRejectsUnauthenticatedNonLoopbackListener(t *testing.T) {
	_, err := Start(Options{
		Listen: "0.0.0.0:0",
		Source: func(context.Context) ([]Tool, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("non-loopback listener started without token authentication")
	}
}

func TestServerRejectsUnsupportedMethod(t *testing.T) {
	server := startTestServer(t, Options{
		Listen: "127.0.0.1:0",
		Source: func(context.Context) ([]Tool, error) { return nil, nil },
	})

	req, err := http.NewRequest(http.MethodPost, "http://"+server.Addr()+ToolsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("status = %d, Allow = %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
}

func getSnapshot(t *testing.T, server *Server, token string) Snapshot {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+server.Addr()+ToolsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var snapshot Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
