package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// InstallMeta describes the source of an install for the manifest.
type InstallMeta struct {
	Version string
	Asset   string
	By      string // "self install" | "self update"
}

// backupSuffix is appended to a replaced file/dir while the transaction is in
// flight. It is per-process and per-call so concurrent or repeated runs never
// collide, and so opportunistic cleanup can recognise our leftovers.
func backupSuffix() string {
	return fmt.Sprintf(".llmtui-bak-%d-%d", os.Getpid(), time.Now().UnixNano())
}

// Install applies a staged Payload to target as a transaction: nothing in the
// live installation is disturbed until the staged binary has been validated,
// and any failure after the first replacement rolls every replacement back.
//
// requireRuntime forces the payload to carry a bundled runtime (true for
// `self update` on a platform that ships one; false for installing a bare
// binary).
func (p *Payload) Install(target Target, meta InstallMeta, requireRuntime bool) (err error) {
	if err := ValidateBinary(p.BinPath, runtime.GOOS); err != nil {
		return err
	}
	if requireRuntime && p.RuntimeDir == "" {
		return errors.New("release archive has no bundled runtime; refusing a partial update")
	}

	if err := ensureWritablePrefix(target); err != nil {
		return err
	}

	tx := &transaction{}
	defer func() {
		if err != nil {
			tx.rollback()
		}
	}()

	suffix := backupSuffix()

	// 1. Runtime tree, replaced first: it is the compound step, so if it
	//    fails the simple binary swap has not happened yet.
	if p.RuntimeDir != "" {
		if err := tx.swapPath(p.RuntimeDir, target.RuntimeDir, target.RuntimeDir+suffix); err != nil {
			return fmt.Errorf("install runtime: %w", err)
		}
	}

	// 2. Binary.
	if err := tx.swapPath(p.BinPath, target.BinPath, target.BinPath+suffix); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if err := os.Chmod(target.BinPath, 0o755); err != nil {
		return fmt.Errorf("chmod installed binary: %w", err)
	}

	// 3. Docs — best effort, never fatal.
	if len(p.DocFiles) > 0 {
		if mkErr := os.MkdirAll(target.DocDir, 0o755); mkErr == nil {
			for _, d := range p.DocFiles {
				_ = copyFile(d, filepath.Join(target.DocDir, filepath.Base(d)), 0o644)
			}
		}
	}

	// 4. Manifest — best effort; a missing manifest only downgrades scope
	//    detection to a heuristic.
	manifestErr := WriteManifest(target.ManifestPath, InstallManifest{
		Version:     meta.Version,
		Scope:       string(target.Scope),
		Prefix:      target.Prefix,
		Asset:       meta.Asset,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		InstalledBy: meta.By,
	})

	// 5. Commit: transaction succeeded, discard rollback and clean backups.
	tx.commit()
	syncDir(filepath.Dir(target.BinPath))
	syncDir(filepath.Dir(target.RuntimeDir))

	if manifestErr != nil {
		return fmt.Errorf("installed %s, but writing the install manifest failed: %w", meta.Version, manifestErr)
	}
	return nil
}

// transaction records reversible filesystem replacements.
type transaction struct {
	steps []txStep
}

type txStep struct {
	// live path that was moved aside, and where it went. restore renames
	// backup back to live after removing whatever now occupies live.
	live, backup string
	hadLive      bool
}

// swapPath moves an existing live path (file or dir) to backup, then moves
// staged into its place. Falls back to a recursive copy when live and staged
// are on different filesystems.
func (t *transaction) swapPath(staged, live, backup string) error {
	_, statErr := os.Lstat(live)
	hadLive := statErr == nil
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", live, statErr)
	}
	if err := rejectUnsafeTarget(live); err != nil {
		return err
	}

	if hadLive {
		if err := os.Rename(live, backup); err != nil {
			return fmt.Errorf("move existing %s aside: %w", live, err)
		}
	}
	step := txStep{live: live, backup: backup, hadLive: hadLive}

	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.restoreStep(step)
		return err
	}
	if err := moveOrCopy(staged, live); err != nil {
		t.restoreStep(step)
		return err
	}
	t.steps = append(t.steps, step)
	return nil
}

func (t *transaction) restoreStep(s txStep) {
	_ = os.RemoveAll(s.live)
	if s.hadLive {
		_ = os.Rename(s.backup, s.live)
	}
}

func (t *transaction) rollback() {
	for i := len(t.steps) - 1; i >= 0; i-- {
		t.restoreStep(t.steps[i])
	}
	t.steps = nil
}

func (t *transaction) commit() {
	for _, s := range t.steps {
		if s.hadLive {
			if err := os.RemoveAll(s.backup); err != nil {
				// A backup we cannot delete now (typically the running
				// executable on Windows) is renamed to a *.old marker that
				// opportunistic cleanup removes on a later `self` run.
				_ = os.Rename(s.backup, staleMarker(s.backup))
			}
		}
	}
	t.steps = nil
}

// rejectUnsafeTarget refuses to replace a path that is a symlink: following
// it would let an attacker who can plant a symlink in the install directory
// redirect the write outside the managed tree.
func rejectUnsafeTarget(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil // does not exist yet
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace %s: it is a symlink", path)
	}
	return nil
}

func moveOrCopy(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return fmt.Errorf("move %s -> %s: %w", src, dst, err)
	}
	// Cross-filesystem: recursive copy, then drop the source.
	if err := copyTree(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	_ = os.RemoveAll(src)
	return nil
}

func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		clean := filepath.ToSlash(target)
		if filepath.IsAbs(target) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/") {
			return fmt.Errorf("refusing to copy symlink %s -> %s (must be a bare sibling name)", src, target)
		}
		return os.Symlink(target, dst)
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode().Perm())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func staleMarker(backup string) string {
	return backup + ".old"
}

// CleanStaleBackups removes leftover *.llmtui-bak-* / *.old markers under a
// prefix's bin and lib directories. Best effort, safe to call on every `self`
// invocation.
func CleanStaleBackups(target Target) {
	for _, dir := range []string{filepath.Dir(target.BinPath), filepath.Dir(target.RuntimeDir)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if strings.Contains(name, ".llmtui-bak-") || strings.HasSuffix(name, ".old") {
				_ = os.RemoveAll(filepath.Join(dir, name))
			}
			if strings.HasPrefix(name, ".llmtui-stage-") {
				_ = os.RemoveAll(filepath.Join(dir, name))
			}
		}
	}
}

// ensureWritablePrefix creates the target directory skeleton and probes for
// write permission, translating a permission failure into an actionable
// message.
func ensureWritablePrefix(target Target) error {
	for _, dir := range []string{filepath.Dir(target.BinPath), filepath.Dir(target.RuntimeDir)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return permissionHint(target, err)
		}
	}
	probe := filepath.Join(filepath.Dir(target.BinPath), ".llmtui-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return permissionHint(target, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return nil
}

func permissionHint(target Target, err error) error {
	if !errors.Is(err, fs.ErrPermission) && !isReadOnlyFS(err) {
		return fmt.Errorf("prepare %s: %w", target.Prefix, err)
	}
	switch target.Scope {
	case ScopeSystem:
		return fmt.Errorf("permission denied writing to %s\n\n%s", target.Prefix, elevatedHint())
	default:
		return fmt.Errorf("permission denied writing to %s: %w", target.Prefix, err)
	}
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
