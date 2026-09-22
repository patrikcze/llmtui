package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// writeFileChecked is the shared safe-write implementation behind both
// write_file and edit_file: workspace confinement, blocked-path guardrails,
// the size cap, parent-directory creation, the O_TRUNC write, and the
// display diff. It returns only the diff; callers format their own result
// line.
//
// When expectCurrent is non-nil the write is a surgical edit: the file must
// already exist, be readable within the cap, and hold exactly the bytes the
// edit was computed against. Any mismatch fails the write untouched so a
// concurrent external change is never silently clobbered.
func (r *Runner) writeFileChecked(rel, content string, expectCurrent *string) (diff string, meta ResultMeta, err error) {
	// The write either fully replaces the file's content or fails outright —
	// there is no partial-content mechanism to be incomplete about.
	meta.Coverage = Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, ObservedBytes: int64(len(content)), RetainedBytes: int64(len(content))}
	meta.Effect = EffectNone // nothing attempted yet at every early-return below
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", meta, withCode(fmt.Errorf("write_file needs a path"), "invalid_arguments", RetryCorrectInput)
	}
	rel = filepath.Clean(rel)
	displayPath := filepath.ToSlash(rel)
	// Block writes into .git (a hook would execute on the next git command),
	// key-material directories, and shell startup files.
	if msg := r.Guardrails.checkWritePath(rel); msg != "" {
		return "", meta, withCode(errors.New(msg), "safety_block", RetryCorrectInput)
	}
	if len(content) > r.maxKB*1024 {
		return "", meta, withCode(fmt.Errorf("content exceeds the %d KB write limit", r.maxKB), "unsupported_content", RetryCorrectInput)
	}
	if _, rerr := r.resolve(rel); rerr != nil {
		return "", meta, withCode(rerr, "safety_block", RetryCorrectInput)
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", meta, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	// Capture the previous content so the TUI can show what changed.
	existed := false
	oldContent := ""
	oldTooBig := false
	if info, err := root.Stat(rel); err == nil {
		if info.IsDir() {
			return "", meta, withCode(fmt.Errorf("%q is a directory", rel), "invalid_arguments", RetryCorrectInput)
		}
		existed = true
		if info.Size() <= int64(r.maxKB)*1024 {
			if data, rerr := readRootFileLimited(root, rel, int64(r.maxKB)*1024); rerr == nil {
				oldContent = string(data)
			} else {
				oldTooBig = true // unreadable: treat like undiffable
			}
		} else {
			oldTooBig = true
		}
	}
	if expectCurrent != nil {
		if !existed {
			return "", meta, withCode(fmt.Errorf("%q no longer exists; use write_file to create it", displayPath), "not_found", RetryReread)
		}
		if oldTooBig {
			return "", meta, withCode(fmt.Errorf("%q changed and is no longer readable within the %d KB limit; re-read it and retry", displayPath, r.maxKB), "unsupported_content", RetryReread)
		}
		if oldContent != *expectCurrent {
			return "", meta, withCode(fmt.Errorf("%q changed since it was read; re-read the file and retry the edit against its current text", displayPath), "match_not_found", RetryReread)
		}
	}
	// From here on, a failure occurs while a write may already be underway —
	// its effect on disk is genuinely unknown, not "none".
	meta.Effect = EffectUnknown
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return "", meta, fmt.Errorf("create parent directory: %w", err)
	}
	file, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", meta, fmt.Errorf("open file for writing: %w", err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		_ = file.Close()
		return "", meta, fmt.Errorf("write file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", meta, fmt.Errorf("close written file: %w", err)
	}
	meta.Outcome = OutcomeOK
	meta.Effect = EffectChanged
	if oldTooBig {
		return fmt.Sprintf("Update(%s) — previous content replaced (too large to diff)", displayPath), meta, nil
	}
	rendered := RenderWriteDiff(displayPath, oldContent, content, existed)
	if IsNoChangeDiff(rendered) {
		meta.Effect = EffectUnchanged
	}
	return rendered, meta, nil
}
