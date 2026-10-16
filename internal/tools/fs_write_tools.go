package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the two structural workspace mutations that complement
// write_file and edit_file: create_directory and move_file. Both share
// write_file's admission rules — the write guardrails (checkWritePath),
// lexical plus symlink-evaluated confinement (Runner.resolve, which walks
// up to the deepest existing ancestor so a not-yet-existing path inside a
// symlinked directory is still checked), and write admission's refusal to
// act through any symlink component (rejectSymlinkWriteTarget). move_file
// applies all of them to both its source and its destination, and neither
// tool deletes anything: a move refuses an existing destination instead of
// replacing it.

type moveFileArgs struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// decodeMoveFileBody accepts {"source":"…","destination":"…"}; the source
// may instead be the fence's path argument.
func decodeMoveFileBody(call *Call) {
	body := strings.TrimSpace(call.Body)
	call.Body = ""
	if len(body) > maxFSToolPayloadBytes {
		call.InputErr = fmt.Sprintf("move_file arguments exceed the %d byte limit", maxFSToolPayloadBytes)
		return
	}
	var args moveFileArgs
	if err := decodeOneJSONObject(body, &args); err != nil {
		call.InputErr = "move_file needs one JSON object {\"source\":\"…\",\"destination\":\"…\"} in the block body: " + err.Error()
		return
	}
	if args.Source != "" {
		if call.Path != "" && filepathCleanSlash(call.Path) != filepathCleanSlash(args.Source) {
			call.InputErr = "move_file source is specified both in the fence and JSON body"
			return
		}
		call.Path = args.Source
	}
	call.Path, call.Destination = strings.TrimSpace(call.Path), strings.TrimSpace(args.Destination)
	if err := ValidateMoveFileCall(call); err != nil {
		call.InputErr = err.Error()
	}
}

// ValidateMoveFileCall checks move_file's argument shape without I/O.
func ValidateMoveFileCall(call *Call) error {
	src, dst := strings.TrimSpace(call.Path), strings.TrimSpace(call.Destination)
	if src == "" || dst == "" {
		return errors.New("move_file needs both source and destination")
	}
	if filepath.Clean(src) == "." || filepath.Clean(dst) == "." {
		return errors.New("move_file cannot move the workspace root or onto it")
	}
	if filepath.Clean(src) == filepath.Clean(dst) {
		return errors.New("move_file source and destination are the same path")
	}
	return nil
}

// checkMutationPath applies write admission to one path: the write
// guardrails and symlink-evaluated confinement. It returns the cleaned
// root-relative name.
func (r *Runner) checkMutationPath(tool, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", withCode(fmt.Errorf("%s needs a path", tool), "invalid_arguments", RetryCorrectInput)
	}
	clean := filepath.Clean(rel)
	if clean == "." {
		return "", withCode(fmt.Errorf("%s cannot act on the workspace root itself", tool), "invalid_arguments", RetryCorrectInput)
	}
	if msg := r.Guardrails.checkWritePath(clean); msg != "" {
		return "", withCode(errors.New(msg), "safety_block", RetryCorrectInput)
	}
	if _, err := r.resolve(clean); err != nil {
		return "", withCode(err, "safety_block", RetryCorrectInput)
	}
	return clean, nil
}

// createDirectory makes rel and any missing parents. An existing directory
// is success with EffectUnchanged; an existing non-directory is an error.
func (r *Runner) createDirectory(c Call) (output string, meta ResultMeta, err error) {
	meta.Effect = EffectNone
	rel, err := r.checkMutationPath(ToolCreateDirectory, c.Path)
	if err != nil {
		return "", meta, err
	}
	display := filepath.ToSlash(rel)
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", meta, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if serr := rejectSymlinkWriteTarget(root, rel); serr != nil {
		return "", meta, withCode(serr, "symlink_write_unsupported", RetryCorrectInput)
	}
	if info, statErr := root.Lstat(rel); statErr == nil {
		if !info.IsDir() {
			return "", meta, withCode(fmt.Errorf("%q already exists and is not a directory", display), "invalid_arguments", RetryCorrectInput)
		}
		meta.Outcome = OutcomeOK
		meta.Effect = EffectUnchanged
		return fmt.Sprintf("directory %s already exists", display), meta, nil
	}
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return "", meta, fmt.Errorf("create directory: %w", err)
	}
	meta.Outcome = OutcomeOK
	meta.Effect = EffectChanged
	return fmt.Sprintf("created directory %s", display), meta, nil
}

// moveBeforeRename is a package-local test seam that runs after every
// admission check and before the rename itself. Production leaves it nil.
var moveBeforeRename func()

// moveFile renames source to destination inside the workspace. Both ends go
// through write admission. The move never replaces an existing destination
// (the platform rename refuses it atomically where available) and never
// moves a symlink. Renaming a likely-secret file to a name that no longer
// looks secret is refused, because the secret-read approval keys on the
// logical name and the rename would silently launder it.
func (r *Runner) moveFile(c Call) (output string, meta ResultMeta, err error) {
	meta.Effect = EffectNone
	if verr := ValidateMoveFileCall(&c); verr != nil {
		return "", meta, withCode(verr, "invalid_arguments", RetryCorrectInput)
	}
	src, err := r.checkMutationPath(ToolMoveFile, c.Path)
	if err != nil {
		return "", meta, err
	}
	dst, err := r.checkMutationPath(ToolMoveFile, c.Destination)
	if err != nil {
		return "", meta, err
	}
	srcDisplay, dstDisplay := filepath.ToSlash(src), filepath.ToSlash(dst)
	secretGuard := r.Guardrails.ProtectSecretFiles || r.Guardrails.RequireApprovalForSecretReads
	if secretGuard && IsSecretPath(src) && !IsSecretPath(dst) {
		return "", meta, withCode(fmt.Errorf("moving secret file %q to %q would hide it from secret-file protection; rename it manually if intended", srcDisplay, dstDisplay), "safety_block", RetryNone)
	}
	if dst == src || strings.HasPrefix(dst+string(filepath.Separator), src+string(filepath.Separator)) {
		return "", meta, withCode(fmt.Errorf("cannot move %q into itself (%q)", srcDisplay, dstDisplay), "invalid_arguments", RetryCorrectInput)
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", meta, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	for _, p := range []string{src, dst} {
		if serr := rejectSymlinkWriteTarget(root, p); serr != nil {
			return "", meta, withCode(errors.New(strings.Replace(serr.Error(), "write_file/edit_file do not write", "move_file does not move", 1)), "symlink_write_unsupported", RetryCorrectInput)
		}
	}
	srcInfo, err := root.Lstat(src)
	if err != nil {
		return "", meta, withCode(fmt.Errorf("move_file source %q: %w", srcDisplay, err), "not_found", RetryCorrectInput)
	}
	if !srcInfo.IsDir() && !srcInfo.Mode().IsRegular() {
		return "", meta, withCode(fmt.Errorf("%q is not a regular file or directory", srcDisplay), "unsupported_content", RetryCorrectInput)
	}
	if _, statErr := root.Lstat(dst); statErr == nil {
		return "", meta, withCode(fmt.Errorf("destination %q already exists; move_file never overwrites — choose another name", dstDisplay), "destination_exists", RetryCorrectInput)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", meta, fmt.Errorf("move_file destination %q: %w", dstDisplay, statErr)
	}
	if err := root.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", meta, fmt.Errorf("create destination directory: %w", err)
	}
	if moveBeforeRename != nil {
		moveBeforeRename()
	}
	if err := renameNoReplace(root, r.root, src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", meta, withCode(fmt.Errorf("destination %q already exists; move_file never overwrites — choose another name", dstDisplay), "destination_exists", RetryCorrectInput)
		}
		return "", meta, fmt.Errorf("move_file: %w", err)
	}
	meta.Outcome = OutcomeOK
	meta.Effect = EffectChanged
	kind := "file"
	if srcInfo.IsDir() {
		kind = "directory"
	}
	return fmt.Sprintf("moved %s %s to %s", kind, srcDisplay, dstDisplay), meta, nil
}
