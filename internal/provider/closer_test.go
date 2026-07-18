package provider

import (
	"context"
	"errors"
	"testing"
)

type plainProvider struct{}

func (plainProvider) Name() string                                    { return "plain" }
func (plainProvider) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }
func (plainProvider) Chat(context.Context, ChatRequest) (<-chan ChatEvent, error) {
	return nil, errors.New("unused")
}
func (plainProvider) HealthCheck(context.Context) error { return nil }

type closableProvider struct {
	plainProvider
	closed int
	err    error
}

func (c *closableProvider) Close() error {
	c.closed++
	return c.err
}

func (c *closableProvider) RuntimeFingerprint() string { return "model.gguf|123|456" }

func TestCloseProviderNoopForPlainProviders(t *testing.T) {
	if err := CloseProvider(plainProvider{}); err != nil {
		t.Fatalf("CloseProvider(plain) = %v, want nil", err)
	}
	if err := CloseProvider(nil); err != nil {
		t.Fatalf("CloseProvider(nil) = %v, want nil", err)
	}
}

func TestCloseProviderClosesAndPropagatesError(t *testing.T) {
	c := &closableProvider{err: errors.New("unload failed")}
	if err := CloseProvider(c); !errors.Is(err, c.err) {
		t.Fatalf("CloseProvider = %v, want %v", err, c.err)
	}
	if c.closed != 1 {
		t.Fatalf("Close called %d times, want 1", c.closed)
	}
}

func TestRuntimeFingerprintOf(t *testing.T) {
	if got := RuntimeFingerprintOf(plainProvider{}); got != "" {
		t.Fatalf("plain provider fingerprint = %q, want empty", got)
	}
	if got := RuntimeFingerprintOf(&closableProvider{}); got != "model.gguf|123|456" {
		t.Fatalf("fingerprint = %q", got)
	}
}
