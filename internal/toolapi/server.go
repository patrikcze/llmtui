// Package toolapi exposes the active llmtui tool catalog over a read-only HTTP API.
package toolapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	APIVersion = "v1"
	ToolsPath  = "/api/v1/tools"
)

// Tool is one capability currently provisionable to the active model.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Source      string          `json:"source"`
	Safety      string          `json:"safety"`
	Approval    string          `json:"approval"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Snapshot is a point-in-time view of the exact active tool catalog.
type Snapshot struct {
	APIVersion string    `json:"api_version"`
	Generated  time.Time `json:"generated_at"`
	Tools      []Tool    `json:"tools"`
}

// Source returns the current tool catalog. Implementations must honor ctx.
type Source func(ctx context.Context) ([]Tool, error)

// Options configures the registry listener.
type Options struct {
	Listen   string
	TokenEnv string
	Source   Source
}

// Server owns one running registry listener.
type Server struct {
	http     *http.Server
	listener net.Listener
	errors   chan error
}

// Start validates the exposure policy, binds the listener, and starts serving.
func Start(opts Options) (*Server, error) {
	if opts.Source == nil {
		return nil, errors.New("tool registry: source is required")
	}
	listen := strings.TrimSpace(opts.Listen)
	if listen == "" {
		return nil, errors.New("tool registry: listen address is required")
	}
	token, err := registryToken(opts.TokenEnv)
	if err != nil {
		return nil, err
	}
	if token == "" && !isLoopbackAddress(listen) {
		return nil, fmt.Errorf("tool registry: non-loopback listen address %q requires token_env", listen)
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("tool registry: listen on %s: %w", listen, err)
	}
	mux := http.NewServeMux()
	mux.Handle(ToolsPath, toolsHandler(opts.Source, token))
	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	server := &Server{http: httpServer, listener: listener, errors: make(chan error, 1)}
	go func() {
		err := httpServer.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			server.errors <- err
		}
		close(server.errors)
	}()
	return server, nil
}

// Addr returns the bound listener address. It is useful when port 0 is used.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Errors reports an unexpected serving failure and closes on normal shutdown.
func (s *Server) Errors() <-chan error { return s.errors }

// Shutdown gracefully stops accepting requests and drains active handlers.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.http.Shutdown(ctx); err != nil {
		_ = s.http.Close()
		return err
	}
	return nil
}

func registryToken(envName string) (string, error) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return "", nil
	}
	token := os.Getenv(envName)
	if token == "" {
		return "", fmt.Errorf("tool registry: token environment variable %s is empty or unset", envName)
	}
	return token, nil
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func toolsHandler(source Source, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if token != "" && !validBearerToken(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="llmtui-tool-registry"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		catalog, err := source(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "tool catalog unavailable")
			return
		}
		if catalog == nil {
			catalog = []Tool{}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Snapshot{APIVersion: APIVersion, Generated: time.Now().UTC(), Tools: catalog})
	})
}

func validBearerToken(header, want string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
