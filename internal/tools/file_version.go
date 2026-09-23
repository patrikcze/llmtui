package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/patrikcze/llmtui/internal/entity"
)

// resolveExpectedVersion turns the model-facing resource selector into the
// exact complete file version that the write operation must still match. The
// resource body is reopened at execution time so an approval-time pin cannot
// hide eviction, kind changes, or a path mismatch.
func (r *Runner) resolveExpectedVersion(ctx context.Context, path, resourceID string, expected *entity.FileVersion) (*entity.FileVersion, error) {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		if expected == nil {
			return nil, nil
		}
		if !expected.Complete || expected.Digest == "" {
			return nil, withCode(fmt.Errorf("the expected file version is incomplete; re-read the whole file"), "snapshot_incomplete", RetryReread)
		}
		if filepath.ToSlash(filepath.Clean(strings.TrimSpace(path))) != filepath.ToSlash(filepath.Clean(strings.TrimSpace(expected.Path))) {
			return nil, withCode(fmt.Errorf("the expected file version belongs to %q, not %q", expected.Path, path), "invalid_arguments", RetryCorrectInput)
		}
		version := *expected
		return &version, nil
	}
	if r.Resources == nil {
		return nil, withCode(fmt.Errorf("expected_resource_id %q is unavailable; re-read the file", resourceID), "resource_unavailable", RetryReread)
	}
	id, err := entity.ParseID(resourceID)
	if err != nil {
		return nil, withCode(fmt.Errorf("invalid expected_resource_id %q: %w", resourceID, err), "invalid_arguments", RetryCorrectInput)
	}
	view, lease, err := r.Resources.OpenBody(ctx, id)
	if err != nil {
		code := "resource_unavailable"
		if strings.Contains(strings.ToLower(err.Error()), "lifetime has ended") {
			code = "resource_expired"
		} else if errors.Is(err, entity.ErrNotAResourceBody) {
			code = "wrong_resource_kind"
		}
		return nil, withCode(fmt.Errorf("expected_resource_id %q cannot be used: %w", resourceID, err), code, RetryReread)
	}
	defer func() { _ = lease.Close() }()
	if view.Kind != entity.KindFile {
		return nil, withCode(fmt.Errorf("expected_resource_id %q refers to %s, not a file snapshot", resourceID, view.Kind), "wrong_resource_kind", RetryCorrectInput)
	}
	version := view.Resource.FileVersion
	if version == nil || !version.Complete || version.Digest == "" {
		return nil, withCode(fmt.Errorf("expected_resource_id %q is not a complete file snapshot; re-read the whole file", resourceID), "snapshot_incomplete", RetryReread)
	}
	if filepath.ToSlash(filepath.Clean(strings.TrimSpace(path))) != filepath.ToSlash(filepath.Clean(strings.TrimSpace(version.Path))) {
		return nil, withCode(fmt.Errorf("expected_resource_id %q belongs to %q, not %q", resourceID, version.Path, path), "invalid_arguments", RetryCorrectInput)
	}
	resolved := *version
	if expected != nil && (expected.Path != resolved.Path || expected.Digest != resolved.Digest || expected.SizeBytes != resolved.SizeBytes || !expected.Complete) {
		return nil, withCode(fmt.Errorf("expected_resource_id %q no longer matches the observed file version; re-read the file", resourceID), "stale_source", RetryReread)
	}
	return &resolved, nil
}
