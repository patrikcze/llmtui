package provider

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestDefaultCapabilities(t *testing.T) {
	c := DefaultCapabilities()
	if !c.SupportsStreaming || !c.SupportsSystemPrompt {
		t.Errorf("defaults = %+v, want streaming + system prompt", c)
	}
	if c.SupportsModelList || c.SupportsTokenUsage || c.SupportsJSONMode {
		t.Errorf("defaults = %+v, should be conservative about optional features", c)
	}
}

func TestRetryableError(t *testing.T) {
	retryable := []error{
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		context.DeadlineExceeded,
		fmt.Errorf("dial tcp: connection refused"),
		fmt.Errorf("unexpected EOF"),
	}
	for _, err := range retryable {
		if !RetryableError(err) {
			t.Errorf("RetryableError(%v) = false, want true", err)
		}
	}

	notRetryable := []error{
		nil,
		context.Canceled,
		errors.New("chat request: status 404: model not found"),
		errors.New("invalid request"),
	}
	for _, err := range notRetryable {
		if RetryableError(err) {
			t.Errorf("RetryableError(%v) = true, want false", err)
		}
	}
}
