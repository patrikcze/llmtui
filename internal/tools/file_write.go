package tools

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/patrikcze/llmtui/internal/entity"
)

// This file is the write **publication primitive** shared by write_file and
// edit_file (Phase 1b of the next-generation tool runtime plan,
// .claude/tasks/plans/next-generation-tool-runtime.md §17/§31, evidence row
// L4). It replaces the previous direct O_CREATE|O_WRONLY|O_TRUNC open on the
// target path with a confined stage-then-atomic-rename sequence: content is
// written to a random, initially owner-only sibling file under the same
// *os.Root as the eventual target, verified, and only then published over
// the real name with (*os.Root).Rename — which os.Root re-validates for
// confinement exactly like every other Root method (see "go doc os.Root").
// A failed or interrupted staging attempt never touches the original file's
// bytes.
//
// Everything before staging — guardrail checks, the size cap, path
// resolution, and the pre-write expectCurrent comparison — is unchanged
// Phase 1a behavior and lives in writeFileChecked below; this file only
// replaces the final "commit bytes to disk" step.
//
// Honest concurrency guarantee (§17): llmtui-controlled writes serialize
// through Runner.ExecuteContext's single-slot execution channel (confirmed
// in phase-1b-report.md — production code reaches writeFileChecked only
// through that path, so no second in-package lock is added here). A stale
// version detected before publish, or introduced during staging, is
// rejected. Failed staging never corrupts the original. Readers see old or
// new content on supported filesystems, never a torn mix. What this does
// NOT provide: compare-and-swap against an arbitrary external writer. A
// process outside llmtui's control can still modify the target after this
// process's last check (see stagedBeforeRename below) or after publish;
// post-write verification (below) can detect some such interference but
// cannot undo it safely. That residual is documented, not hidden — see the
// tests beside this file for what is and is not covered, and
// docs/tools-architecture.md / docs/security.md for the model-facing
// wording.

// stagedFileCreate, stagedFileWrite, stagedFileSync, and stagedFileClose
// indirect the staging file's Create/Write/Sync/Close calls so tests can
// inject a failure at each seam without a model-reachable control or
// conditionally-compiled production code. Production always calls through
// these; only *_test.go files in this package ever reassign them, and every
// reassignment is restored (via t.Cleanup) before the test returns.
var (
	stagedFileCreate = func(root *os.Root, name string, perm os.FileMode) (*os.File, error) {
		return root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	}
	stagedFileWrite = func(f *os.File, p []byte) (int, error) { return f.Write(p) }
	stagedFileSync  = func(f *os.File) error { return f.Sync() }
	stagedFileClose = func(f *os.File) error { return f.Close() }
)

// stagedBeforeRecheck and stagedBeforeRename are no-ops in production and
// exist purely as deterministic test seams for the two race windows the
// honest concurrency guarantee above documents:
//
//   - stagedBeforeRecheck runs immediately before the step-5 re-read of the
//     current on-disk target, after staging is fully written, synced, and
//     closed. Tests use it to simulate an external writer that changed the
//     file during staging; the recheck must then reject the publish and
//     leave the external writer's bytes intact.
//   - stagedBeforeRename runs immediately after that recheck passes and
//     immediately before (*os.Root).Rename is called. A write here
//     simulates the one race this mechanism cannot close: an external
//     writer landing in the gap between this process's last check and the
//     rename syscall. Rename still executes and still atomically replaces
//     whatever is there at that instant — this process's content wins,
//     deterministically, never a torn file — but the interloper's write is
//     silently lost. That is the documented residual; it is not something a
//     single optimistic recheck run from inside one process can close.
var (
	stagedBeforeRecheck = func() {}
	stagedBeforeRename  = func() {}
)

// stagedRename, syncParentDirFn, and verifyPublishedContentFn are the same
// kind of test-only indirection as the staging hooks above, covering the
// three steps after staging completes: the publish itself, the best-effort
// post-publish directory sync, and the post-publish readback confirmation.
// syncParentDirFn and verifyPublishedContentFn default to the real
// functions defined elsewhere in this package (syncParentDir is platform-
// split; verifyPublishedContentFn wraps this file's own
// verifyPublishedContent) so overriding one in a test never has to
// reimplement the real behavior, only replace it for that one test.
var (
	stagedRename = func(root *os.Root, oldname, newname string) error {
		return root.Rename(oldname, newname)
	}
	syncParentDirFn          = syncParentDir
	verifyPublishedContentFn = verifyPublishedContent
)

// stagingFilePrefix marks a staged write's sibling file as llmtui's own, so
// a crash that leaves one behind is recognizable and not mistaken for
// model-authored content.
const stagingFilePrefix = ".llmtui-write-"

// maxStagingNameAttempts bounds retries of the random staging filename on an
// O_EXCL collision. A collision is astronomically unlikely with 16 random
// hex bytes; this only guards against a degenerate crypto/rand failure mode
// repeating the same bytes, not a realistic collision.
const maxStagingNameAttempts = 8

// rejectSymlinkWriteTarget rejects a write whose target, or any existing
// parent directory component, is a symbolic link. It walks from the
// workspace root down to rel's immediate parent and finally rel itself,
// Lstat-ing each existing prefix so a symlink is never silently written
// through or replaced by the write path — existing reads may still follow a
// confined in-workspace symlink (r.resolve's checkSymlinkEscape); this is
// additional, write-only admission: replacing a symlink's target by
// renaming over the link name is exactly the ambiguity this phase's write
// path declines to resolve implicitly, even when the link stays inside the
// workspace.
//
// Components that do not exist yet are not symlinks — there is nothing
// there — so they are skipped: root.MkdirAll only ever creates real
// directories, and a not-yet-existing target is the ordinary create case.
//
// This does not detect a hard link to a file outside this workspace; see
// checkNotHardLinked (file_replace_unix.go / file_replace_windows.go) for
// the separate, best-effort hard-link check and its own documented limits.
// os.Root confines by path, not by inode or link identity, and enforces
// neither symlink- nor hard-link-exclusivity on its own — this function and
// checkNotHardLinked are this package's own, best-effort defenses on top of
// it, not a claim that os.Root already provides them.
func rejectSymlinkWriteTarget(root *os.Root, rel string) error {
	clean := filepath.Clean(rel)
	if clean == "." {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	var built string
	for _, part := range parts {
		if built == "" {
			built = part
		} else {
			built = built + "/" + part
		}
		info, err := root.Lstat(built)
		if errors.Is(err, os.ErrNotExist) {
			return nil // this and every deeper component are new
		}
		if err != nil {
			return fmt.Errorf("check %q for a symlink: %w", built, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symlink (or has a symlinked parent directory); write_file/edit_file do not write through symlinks — replace the link manually or point the tool at the real path", filepath.ToSlash(rel))
		}
	}
	return nil
}

// publishOutcome carries the result of a successful confined publish. The
// zero value means the publish is fully confirmed with nothing to report;
// DurabilityWarning is set only when Rename already succeeded but a
// best-effort post-publish step (directory sync, readback) could not
// confirm it — the write is committed either way.
type publishOutcome struct {
	DurabilityWarning string
}

// publishStagedWrite performs steps 3-7 of the confined write algorithm
// (next-generation-tool-runtime.md §17): stage content in a random,
// initially owner-only sibling under root, set the target's real
// permission bits on the staged file handle (never a path-based chmod —
// that races if the target changes identity mid-operation), verify the
// staged write completed in full, optionally recheck the live target
// against expectCurrent immediately before publish (closing as much of the
// L4 final-race window as a single optimistic check can — see
// stagedBeforeRename's doc comment for what remains open), publish with
// Root.Rename, and best-effort confirm afterward.
//
// A non-nil error means the file at rel is untouched: every failure path
// here either never reached Rename, or Rename itself failed — and
// (*os.Root).Rename, like the platform rename primitive it wraps, either
// fully replaces the target or leaves it alone; there is no partial state
// to reason about. Callers should treat any error return as Effect=none.
//
// A nil error means Rename already succeeded: the target now holds content.
// The returned publishOutcome.DurabilityWarning, if non-empty, describes a
// best-effort confirmation step that could not run or disagreed — it is
// informational, not a sign the write itself failed. Callers should treat a
// nil error as Effect=changed unconditionally, warning or not.
func publishStagedWrite(root *os.Root, rel string, content []byte, mode os.FileMode, expectCurrent *string, byteLimit int64) (out publishOutcome, err error) {
	dir := filepath.Dir(rel)
	base := filepath.Base(rel)

	var file *os.File
	var tempName string
	var createErr error
	for attempt := 0; attempt < maxStagingNameAttempts; attempt++ {
		suffix, herr := randomHex(8)
		if herr != nil {
			return out, fmt.Errorf("generate staging file name: %w", herr)
		}
		candidate := stagingFilePrefix + base + "." + suffix + ".tmp"
		if dir != "." {
			candidate = dir + "/" + candidate
		}
		f, oerr := stagedFileCreate(root, candidate, 0o600)
		if oerr == nil {
			file, tempName = f, candidate
			createErr = nil
			break
		}
		createErr = oerr
		if !errors.Is(oerr, os.ErrExist) {
			break
		}
	}
	if file == nil {
		return out, fmt.Errorf("create staging file: %w", createErr)
	}

	// tempName exists from here on until either Rename publishes it (moving
	// it to rel) or this function returns with an error; the deferred
	// cleanup below removes it in every non-published case, including a
	// panic unwind.
	published := false
	defer func() {
		if !published {
			_ = root.Remove(tempName)
		}
	}()

	// Set the real target permission bits on the open staging handle (an
	// fchmod-equivalent) rather than a path-based chmod after the fact,
	// which would race if something else replaced the path's identity
	// between the chmod and the write. Done before the write, not after, so
	// a chmod failure is caught before this function has spent the cost of
	// writing the full content.
	if cerr := file.Chmod(mode); cerr != nil {
		_ = file.Close()
		return out, fmt.Errorf("set staging file permissions: %w", cerr)
	}

	n, werr := stagedFileWrite(file, content)
	if werr == nil && n != len(content) {
		werr = io.ErrShortWrite
	}
	if werr != nil {
		_ = file.Close()
		return out, fmt.Errorf("write staging file: %w", werr)
	}
	if serr := stagedFileSync(file); serr != nil {
		_ = file.Close()
		return out, fmt.Errorf("sync staging file: %w", serr)
	}
	if cerr := stagedFileClose(file); cerr != nil {
		return out, fmt.Errorf("close staging file: %w", cerr)
	}

	if expectCurrent != nil {
		// A plain overwrite (expectCurrent == nil, e.g. write_file's own
		// call) has nothing to recheck against — writeFileChecked's earlier,
		// pre-staging comparison is the only staleness signal it ever had,
		// same as before Phase 1b. Only a precondition-carrying write
		// (edit_file, always; write_file, when it is given one) reaches
		// this recheck.
		stagedBeforeRecheck()
		current, rerr := readRootFileLimited(root, rel, byteLimit)
		if rerr != nil {
			return out, fmt.Errorf("recheck current file before publish: %w", rerr)
		}
		if string(current) != *expectCurrent {
			return out, withCode(fmt.Errorf("%q changed during staging; re-read the file and retry the edit against its current text", filepath.ToSlash(rel)), "match_not_found", RetryReread)
		}
	}

	// This is the one race the recheck above cannot close: an external
	// writer landing right here, after the recheck passed but before Rename
	// actually runs. See stagedBeforeRename's doc comment for what happens
	// when that race is lost (this process's content still wins,
	// deterministically — the interloper's write is what gets silently
	// discarded, not this one) and the honest-guarantee comment at the top
	// of this file for why no amount of single-process optimistic checking
	// closes it completely.
	stagedBeforeRename()
	if rerr := stagedRename(root, tempName, rel); rerr != nil {
		return out, fmt.Errorf("publish staged write: %w", rerr)
	}
	published = true

	if serr := syncParentDirFn(root, dir); serr != nil {
		out.DurabilityWarning = fmt.Sprintf("directory sync after publish failed (%v); the write is committed but crash durability is reduced", serr)
	}
	if verr := verifyPublishedContentFn(root, rel, content, byteLimit); verr != nil {
		note := fmt.Sprintf("post-write confirmation read failed (%v); the write is committed but could not be re-verified", verr)
		if out.DurabilityWarning != "" {
			out.DurabilityWarning += "; " + note
		} else {
			out.DurabilityWarning = note
		}
	}
	return out, nil
}

// verifyPublishedContent reopens the just-published target and confirms it
// holds exactly what was intended (step 7's readback). A mismatch here does
// not mean this process's write failed — Rename already committed it — it
// means either the read itself failed or something else wrote to the target
// in the narrow window between Rename and this readback (the documented
// external-writer-after-final-check residual). Either way the caller
// reports it as a durability-only warning, never as a failed write.
func verifyPublishedContent(root *os.Root, rel string, want []byte, byteLimit int64) error {
	got, err := readRootFileLimited(root, rel, byteLimit)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("published content does not match what was written (observed %d bytes, wrote %d)", len(got), len(want))
	}
	return nil
}

// randomHex returns n random bytes hex-encoded, used only to make the
// staging file name unpredictable and collision-free; it carries no
// security property beyond that (the staging file's confidentiality already
// comes from its 0o600 mode and workspace confinement, not from its name
// being hard to guess).
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// writeFileChecked is the shared safe-write implementation behind both
// write_file and edit_file: workspace confinement, blocked-path guardrails,
// the size cap, the pre-write expectCurrent precondition, and the confined
// staged-write-then-atomic-rename publish (publishStagedWrite above). It
// returns only the diff; callers format their own result line.
//
// When expectCurrent is non-nil the write is a surgical edit: the file must
// already exist, be readable within the cap, and hold exactly the bytes the
// edit was computed against both before staging begins and again
// immediately before publish. Any mismatch fails the write untouched. See
// the honest concurrency guarantee documented at the top of this file for
// exactly what that precondition does and does not protect against.
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

	// Write admission is stricter than read admission: a symlink target or a
	// symlinked parent directory component is rejected outright, never
	// silently written through or replaced. Existing reads are unaffected —
	// this check runs only on the write path.
	if serr := rejectSymlinkWriteTarget(root, rel); serr != nil {
		return "", meta, withCode(serr, "symlink_write_unsupported", RetryCorrectInput)
	}

	// Capture the previous content so the TUI can show what changed.
	existed := false
	oldContent := ""
	oldTooBig := false
	existingMode := os.FileMode(0o644)
	if info, statErr := root.Stat(rel); statErr == nil {
		if info.IsDir() {
			return "", meta, withCode(fmt.Errorf("%q is a directory", rel), "invalid_arguments", RetryCorrectInput)
		}
		if hlerr := checkNotHardLinked(info); hlerr != nil {
			return "", meta, withCode(fmt.Errorf("%q %w", displayPath, hlerr), "unsupported_content", RetryNone)
		}
		existed = true
		existingMode = info.Mode().Perm()
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
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return "", meta, fmt.Errorf("create parent directory: %w", err)
	}

	mode := os.FileMode(0o644)
	if existed {
		mode = existingMode
	}
	pubOut, perr := publishStagedWrite(root, rel, []byte(content), mode, expectCurrent, int64(r.maxKB)*1024)
	if perr != nil {
		// Every publishStagedWrite failure path either never reached Rename
		// or Rename itself failed atomically — the target is untouched, so
		// Effect stays at its EffectNone zero value from the top of this
		// function, never "unknown".
		return "", meta, perr
	}
	// Rename already succeeded by the time publishStagedWrite returns nil:
	// the target now holds content regardless of what the best-effort
	// post-publish confirmation below finds.
	meta.Outcome = OutcomeOK
	meta.Effect = EffectChanged
	meta.SourceDigest = digestBytes([]byte(content))
	meta.ContentDigest = meta.SourceDigest
	meta.FileVersion = &entity.FileVersion{Path: displayPath, Digest: meta.SourceDigest, SizeBytes: int64(len(content)), Complete: true}
	meta.Encoding = encodingInfo([]byte(content), true)

	var rendered string
	if oldTooBig {
		rendered = fmt.Sprintf("Update(%s) — previous content replaced (too large to diff)", displayPath)
	} else {
		rendered = RenderWriteDiff(displayPath, oldContent, content, existed)
		if IsNoChangeDiff(rendered) {
			meta.Effect = EffectUnchanged
		}
	}
	if pubOut.DurabilityWarning != "" {
		rendered += "\n(" + pubOut.DurabilityWarning + ")"
	}
	return rendered, meta, nil
}
