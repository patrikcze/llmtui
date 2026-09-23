package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file fault-injects the confined staged-write publication path added
// in Phase 1b (.claude/tasks/plans/next-generation-tool-runtime.md §33,
// §31 checklist bullet 4). Every seam below is a package-level function
// variable (file_write.go) that defaults to the real implementation;
// production code never reads a model-controlled value here, only these
// tests reassign the vars, and every reassignment is restored via
// t.Cleanup before the test returns — never leaking into another test even
// on failure.

// resetStagingHooks restores every staging hook to its default (real)
// implementation after the calling test, regardless of what it overrode.
func resetStagingHooks(t *testing.T) {
	t.Helper()
	origCreate := stagedFileCreate
	origWrite := stagedFileWrite
	origSync := stagedFileSync
	origClose := stagedFileClose
	origBeforeRecheck := stagedBeforeRecheck
	origBeforeRename := stagedBeforeRename
	origRename := stagedRename
	origSyncDir := syncParentDirFn
	origVerify := verifyPublishedContentFn
	t.Cleanup(func() {
		stagedFileCreate = origCreate
		stagedFileWrite = origWrite
		stagedFileSync = origSync
		stagedFileClose = origClose
		stagedBeforeRecheck = origBeforeRecheck
		stagedBeforeRename = origBeforeRename
		stagedRename = origRename
		syncParentDirFn = origSyncDir
		verifyPublishedContentFn = origVerify
	})
}

// assertNoStagingFilesLeftBehind walks root and fails the test if any
// stagingFilePrefix-named file remains — a failed publish must always clean
// up its sibling, and a successful one has nothing left to clean up (Rename
// consumes the name).
func assertNoStagingFilesLeftBehind(t *testing.T, root string) {
	t.Helper()
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.Contains(info.Name(), stagingFilePrefix) {
			t.Errorf("staging file left behind: %s", path)
		}
		return nil
	})
}

func TestPublishStagedWriteCreateFailureLeavesOriginalUntouched(t *testing.T) {
	resetStagingHooks(t)
	injected := errors.New("injected create failure")
	stagedFileCreate = func(root *os.Root, name string, perm os.FileMode) (*os.File, error) {
		return nil, injected
	}
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	r := NewRunner(root, 64)

	diff, meta, err := r.writeFileChecked("f.txt", "new content\n", nil)
	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("err = %v, want it to wrap the injected create failure", err)
	}
	if diff != "" {
		t.Fatalf("diff = %q, want empty on failure", diff)
	}
	if meta.Effect != EffectNone {
		t.Fatalf("Effect = %v, want EffectNone: the target was never touched", meta.Effect)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "original\n" {
		t.Fatalf("file = %q, original was modified", got)
	}
	assertNoStagingFilesLeftBehind(t, root)
}

func TestPublishStagedWriteWriteFailureLeavesOriginalUntouched(t *testing.T) {
	resetStagingHooks(t)
	injected := errors.New("injected write failure")
	stagedFileWrite = func(f *os.File, p []byte) (int, error) { return 0, injected }
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	r := NewRunner(root, 64)

	_, meta, err := r.writeFileChecked("f.txt", "new content\n", nil)
	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("err = %v, want it to wrap the injected write failure", err)
	}
	if meta.Effect != EffectNone {
		t.Fatalf("Effect = %v, want EffectNone", meta.Effect)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "original\n" {
		t.Fatalf("file = %q, original was modified", got)
	}
	assertNoStagingFilesLeftBehind(t, root)
}

func TestPublishStagedWriteShortWriteLeavesOriginalUntouched(t *testing.T) {
	resetStagingHooks(t)
	// A short write with a nil error (the classic "wrote fewer bytes than
	// asked, no error" shape some io.Writers produce) must be treated as a
	// failure, not silently accepted as success.
	stagedFileWrite = func(f *os.File, p []byte) (int, error) {
		if len(p) == 0 {
			return 0, nil
		}
		return len(p) - 1, nil
	}
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	r := NewRunner(root, 64)

	_, meta, err := r.writeFileChecked("f.txt", "new content\n", nil)
	if err == nil || !strings.Contains(err.Error(), "write staging file") {
		t.Fatalf("err = %v, want a short-write failure", err)
	}
	if meta.Effect != EffectNone {
		t.Fatalf("Effect = %v, want EffectNone", meta.Effect)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "original\n" {
		t.Fatalf("file = %q, original was modified", got)
	}
	assertNoStagingFilesLeftBehind(t, root)
}

func TestPublishStagedWriteSyncFailureLeavesOriginalUntouched(t *testing.T) {
	resetStagingHooks(t)
	injected := errors.New("injected sync failure")
	stagedFileSync = func(f *os.File) error { return injected }
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	r := NewRunner(root, 64)

	_, meta, err := r.writeFileChecked("f.txt", "new content\n", nil)
	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("err = %v, want it to wrap the injected sync failure", err)
	}
	if meta.Effect != EffectNone {
		t.Fatalf("Effect = %v, want EffectNone", meta.Effect)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "original\n" {
		t.Fatalf("file = %q, original was modified", got)
	}
	assertNoStagingFilesLeftBehind(t, root)
}

func TestPublishStagedWriteCloseFailureLeavesOriginalUntouched(t *testing.T) {
	resetStagingHooks(t)
	injected := errors.New("injected close failure")
	stagedFileClose = func(f *os.File) error { return injected }
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	r := NewRunner(root, 64)

	_, meta, err := r.writeFileChecked("f.txt", "new content\n", nil)
	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("err = %v, want it to wrap the injected close failure", err)
	}
	if meta.Effect != EffectNone {
		t.Fatalf("Effect = %v, want EffectNone", meta.Effect)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "original\n" {
		t.Fatalf("file = %q, original was modified", got)
	}
	// A close failure happens after the OS-level write; the staging file
	// itself may or may not exist depending on the OS, but the real target
	// must never have been touched and cleanup must not leave a stray file
	// visible under this package's own naming.
	assertNoStagingFilesLeftBehind(t, root)
}

// TestPublishStagedWriteRenameFailureLeavesOriginalUntouched fault-injects
// the publish (rename) step itself — Root.Rename either fully replaces the
// target or leaves it alone; this proves the "leaves it alone" side.
func TestPublishStagedWriteRenameFailureLeavesOriginalUntouched(t *testing.T) {
	resetStagingHooks(t)
	injected := errors.New("injected rename failure")
	stagedRename = func(root *os.Root, oldname, newname string) error { return injected }
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	r := NewRunner(root, 64)

	_, meta, err := r.writeFileChecked("f.txt", "new content\n", nil)
	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("err = %v, want it to wrap the injected rename failure", err)
	}
	if meta.Effect != EffectNone {
		t.Fatalf("Effect = %v, want EffectNone: Rename failed, so nothing published", meta.Effect)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "original\n" {
		t.Fatalf("file = %q, original was modified despite a failed rename", got)
	}
	assertNoStagingFilesLeftBehind(t, root)
}

// TestPublishStagedWriteRecheckRejectsChangeDuringStaging is the new
// step-5 behavior this phase adds: a target mutated by an external writer
// *during* staging (after the pre-staging expectCurrent comparison, before
// the immediately-pre-rename recheck) must be caught by that recheck, not
// published over.
func TestPublishStagedWriteRecheckRejectsChangeDuringStaging(t *testing.T) {
	resetStagingHooks(t)
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	writeTemp(t, root, "f.txt", "one\nTWO\nthree\n")
	r := NewRunner(root, 64)

	stagedBeforeRecheck = func() {
		// Simulate an external process writing to the file while this
		// process's staging file was being written, synced, and closed.
		if err := os.WriteFile(path, []byte("one\nEXTERNAL\nthree\n"), 0o600); err != nil {
			t.Fatalf("simulate external write: %v", err)
		}
	}

	_, meta, err := r.writeFileChecked("f.txt", "one\n2\nthree\n", ptr("one\nTWO\nthree\n"))
	if err == nil {
		t.Fatal("want the recheck to reject a change introduced during staging")
	}
	var ce *classifiedError
	if !errors.As(err, &ce) || ce.code != "match_not_found" {
		t.Fatalf("err = %v, want a match_not_found classified error", err)
	}
	if meta.Effect != EffectNone {
		t.Fatalf("Effect = %v, want EffectNone", meta.Effect)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\nEXTERNAL\nthree\n" {
		t.Fatalf("file = %q, want the external writer's bytes intact", got)
	}
	assertNoStagingFilesLeftBehind(t, root)
}

// TestPublishStagedWriteExternalWriterAfterFinalCheckResidual documents,
// deterministically, the one race this mechanism cannot close (see the
// honest-guarantee comment at the top of file_write.go and
// stagedBeforeRename's doc comment): a writer that lands after the step-5
// recheck passes but before Rename actually runs. This process's rename
// still executes and still atomically replaces whatever is on disk at that
// instant — the outcome is deterministic and never a torn/corrupted file —
// but the interloper's write is what gets silently discarded, not this
// process's. This test asserts that specific, non-corrupted outcome; it
// does not and cannot assert the race "can't happen".
func TestPublishStagedWriteExternalWriterAfterFinalCheckResidual(t *testing.T) {
	resetStagingHooks(t)
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	writeTemp(t, root, "f.txt", "one\nTWO\nthree\n")
	r := NewRunner(root, 64)

	stagedBeforeRename = func() {
		if err := os.WriteFile(path, []byte("one\nINTERLOPER\nthree\n"), 0o600); err != nil {
			t.Fatalf("simulate external write: %v", err)
		}
	}

	_, meta, err := r.writeFileChecked("f.txt", "one\n2\nthree\n", ptr("one\nTWO\nthree\n"))
	if err != nil {
		t.Fatalf("write: %v (the rename itself must still succeed)", err)
	}
	if meta.Effect != EffectChanged {
		t.Fatalf("Effect = %v, want EffectChanged", meta.Effect)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\n2\nthree\n" {
		t.Fatalf("file = %q, want this process's content to have won the race deterministically (interloper's write silently lost, not a torn file)", got)
	}
	assertNoStagingFilesLeftBehind(t, root)
}

// TestPublishStagedWriteDirSyncFailureIsDurabilityWarningNotFailure proves
// that a post-publish directory-sync failure — Rename has already
// committed by this point — is surfaced as an informational warning, never
// as EffectUnknown/failed: the data is genuinely on disk.
func TestPublishStagedWriteDirSyncFailureIsDurabilityWarningNotFailure(t *testing.T) {
	resetStagingHooks(t)
	injected := errors.New("injected dir sync failure")
	syncParentDirFn = func(root *os.Root, dirRel string) error { return injected }
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	r := NewRunner(root, 64)

	diff, meta, err := r.writeFileChecked("f.txt", "new content\n", nil)
	if err != nil {
		t.Fatalf("write: %v (a dir-sync failure must not fail the call)", err)
	}
	if meta.Outcome != OutcomeOK || meta.Effect != EffectChanged {
		t.Fatalf("Outcome/Effect = %v/%v, want OutcomeOK/EffectChanged", meta.Outcome, meta.Effect)
	}
	if !strings.Contains(diff, "directory sync") {
		t.Fatalf("diff = %q, want it to mention the durability warning", diff)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "new content\n" {
		t.Fatalf("file = %q, want the write to have actually landed", got)
	}
}

// TestPublishStagedWriteReadbackFailureIsDurabilityWarningNotFailure is the
// step-7 counterpart: the post-publish confirmation read fails, but Rename
// already succeeded, so this must classify as EffectChanged with a warning,
// never as a failed or unknown-effect write.
func TestPublishStagedWriteReadbackFailureIsDurabilityWarningNotFailure(t *testing.T) {
	resetStagingHooks(t)
	injected := errors.New("injected readback failure")
	verifyPublishedContentFn = func(root *os.Root, rel string, want []byte, byteLimit int64) error { return injected }
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	r := NewRunner(root, 64)

	diff, meta, err := r.writeFileChecked("f.txt", "new content\n", nil)
	if err != nil {
		t.Fatalf("write: %v (a readback failure must not fail the call)", err)
	}
	if meta.Outcome != OutcomeOK || meta.Effect != EffectChanged {
		t.Fatalf("Outcome/Effect = %v/%v, want OutcomeOK/EffectChanged", meta.Outcome, meta.Effect)
	}
	if !strings.Contains(diff, "confirmation read failed") {
		t.Fatalf("diff = %q, want it to mention the durability warning", diff)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "new content\n" {
		t.Fatalf("file = %q, want the write to have actually landed", got)
	}
}

func TestWriteFileRejectsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test skipped on windows")
	}
	root := t.TempDir()
	writeTemp(t, root, "real.txt", "real content\n")
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolWriteFile, Path: "link.txt", Body: "clobbered"})
	if res.Err == nil {
		t.Fatal("write_file through a symlink must be rejected")
	}
	var ce *classifiedError
	if !errors.As(res.Err, &ce) || ce.code != "symlink_write_unsupported" {
		t.Fatalf("err = %v, want a symlink_write_unsupported classified error", res.Err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "real.txt"))
	if string(got) != "real content\n" {
		t.Fatalf("symlink target was modified: %q", got)
	}
}

func TestEditFileRejectsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test skipped on windows")
	}
	root := t.TempDir()
	writeTemp(t, root, "real.txt", "one\ntwo\nthree\n")
	// A relative link target (rather than link.txt -> <absolute path>) so
	// editFile's own pre-writeFileChecked root.Stat (which follows the
	// final symlink component, unlike Lstat) can actually traverse it —
	// os.Root rejects an absolute symlink target outright regardless of
	// where it points, which would otherwise fail this test for an
	// unrelated reason before ever reaching the check this test targets.
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolEditFile, Path: "link.txt", OldText: "two", NewText: "TWO"})
	if res.Err == nil {
		t.Fatal("edit_file through a symlink must be rejected")
	}
	var ce *classifiedError
	if !errors.As(res.Err, &ce) || ce.code != "symlink_write_unsupported" {
		t.Fatalf("err = %v, want a symlink_write_unsupported classified error", res.Err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "real.txt"))
	if string(got) != "one\ntwo\nthree\n" {
		t.Fatalf("symlink target was modified: %q", got)
	}
}

func TestWriteFileRejectsSymlinkedParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test skipped on windows")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "realdir")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolWriteFile, Path: "linkdir/newfile.txt", Body: "leaked"})
	if res.Err == nil {
		t.Fatal("write through a symlinked parent directory must be rejected")
	}
	var ce *classifiedError
	if !errors.As(res.Err, &ce) || ce.code != "symlink_write_unsupported" {
		t.Fatalf("err = %v, want a symlink_write_unsupported classified error", res.Err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "newfile.txt")); err == nil {
		t.Fatal("file was written through the symlinked parent directory")
	}
}

func TestWriteFileRejectsHardLinkedTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard-link Nlink check is a documented Unix-only residual; see file_replace_windows.go")
	}
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "original\n")
	if err := os.Link(filepath.Join(root, "f.txt"), filepath.Join(root, "f2.txt")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolWriteFile, Path: "f.txt", Body: "new content\n"})
	if res.Err == nil {
		t.Fatal("write_file over a hard-linked target must be rejected")
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "original\n" {
		t.Fatalf("file = %q, hard-linked target was modified", got)
	}
	other, _ := os.ReadFile(filepath.Join(root, "f2.txt"))
	if string(other) != "original\n" {
		t.Fatalf("other link = %q, must also be untouched", other)
	}
}

// TestWriteFileNoStagingFileLeftBehindAfterSuccess and
// TestWriteFilePreservesPermissionBitsOnOverwrite expand
// TestEditFileStaleContentGuard's guarantee (edit_file_test.go) per the
// Phase 1b brief: the matching-snapshot path in that test now goes through
// staging+rename, and this confirms the two properties that path promises
// beyond "the write succeeds" — no leftover sibling, and permission bits
// carried over from the pre-existing file rather than reset to the
// create-time default.
func TestWriteFileNoStagingFileLeftBehindAfterSuccess(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "one\nTWO\nthree\n")
	r := NewRunner(root, 64)
	if _, _, err := r.writeFileChecked("f.txt", "one\n2\nthree\n", ptr("one\nTWO\nthree\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "f.txt" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("root entries = %v, want exactly [f.txt]", names)
	}
}

func TestWriteFilePreservesPermissionBitsOnOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on windows")
	}
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("one\nTWO\nthree\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root, 64)
	if _, _, err := r.writeFileChecked("f.txt", "one\n2\nthree\n", ptr("one\nTWO\nthree\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want the pre-existing 0o640 preserved (not reset to the create-time default)", info.Mode().Perm())
	}
}

func TestWriteFileNewFileUsesDefaultMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on windows")
	}
	root := t.TempDir()
	r := NewRunner(root, 64)
	if _, _, _, err := r.writeFileMeta("new.txt", "hello\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want the unchanged 0o644 default for a newly created file", info.Mode().Perm())
	}
}
