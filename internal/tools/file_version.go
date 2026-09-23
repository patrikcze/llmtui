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
// exact complete file version that the write operation must still match.
//
// When expected is already resolved — internal/tui's bindObservedEditVersions
// is the only caller that ever sets it, always from its own record of a
// version already delivered to the model in this conversation, never from
// unverified model input — it is trusted directly, the same way it is when
// no resource ID is attached at all. It is deliberately NOT re-resolved
// through the entity registry even when a resource ID also happens to be
// attached: that registry is a separate, independently-bounded cache
// (internal/entity's own retention/eviction), and requiring its retention
// window to still cover an already-verified version adds an availability
// dependency without adding safety — the write's real protection is
// writeFileMetaExpected's digest comparison against the live file, which
// runs unconditionally regardless of which branch resolved expected, and a
// path mismatch is checked here either way. A bare, model-supplied resource
// ID with no corroborating local record (expected == nil) still goes
// through the full reopen-and-verify path below — that ID has no other
// trust basis.
func (r *Runner) resolveExpectedVersion(ctx context.Context, path, resourceID string, expected *entity.FileVersion) (*entity.FileVersion, error) {
	resourceID = strings.TrimSpace(resourceID)
	if expected != nil {
		if !expected.Complete || expected.Digest == "" {
			return nil, withCode(fmt.Errorf("the expected file version is incomplete; re-read the whole file"), "snapshot_incomplete", RetryReread)
		}
		if filepath.ToSlash(filepath.Clean(strings.TrimSpace(path))) != filepath.ToSlash(filepath.Clean(strings.TrimSpace(expected.Path))) {
			return nil, withCode(fmt.Errorf("the expected file version belongs to %q, not %q", expected.Path, path), "invalid_arguments", RetryCorrectInput)
		}
		version := *expected
		return &version, nil
	}
	if resourceID == "" {
		return nil, nil
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
	return &resolved, nil
}
