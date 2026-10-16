package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file holds the read-only workspace inspection tools that complement
// read_file and list_dir: read_multiple_files, get_file_info, and
// directory_tree. Each resolves its path through Runner.resolve (lexical
// confinement plus symlink-evaluated escape check) and then reads through
// an *os.Root, exactly like read_file, so none of them can observe anything
// outside the workspace.

const (
	// MaxReadMultipleFiles caps how many paths one read_multiple_files call
	// may name.
	MaxReadMultipleFiles = 10
	// DefaultTreeDepth and MaxTreeDepth bound directory_tree's recursion.
	DefaultTreeDepth = 3
	MaxTreeDepth     = 10
	// maxTreeNodes bounds directory_tree's total entries so a huge workspace
	// cannot produce an unbounded tree.
	maxTreeNodes = 2000
	// maxFSToolPayloadBytes bounds the fenced/native argument body of the
	// tools in this file.
	maxFSToolPayloadBytes = 16 * 1024
)

// readMultipleArgs is the fenced JSON body of read_multiple_files.
type readMultipleArgs struct {
	Paths []string `json:"paths"`
}

// directoryTreeArgs is the fenced JSON body of directory_tree.
type directoryTreeArgs struct {
	Path    string   `json:"path"`
	Depth   int      `json:"depth"`
	Exclude []string `json:"exclude"`
}

// setReadPaths validates and stores read_multiple_files' paths on call.
func setReadPaths(call *Call, paths []string) error {
	call.PathCount = 0
	call.Paths = [MaxReadMultipleFiles]string{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if call.PathCount == MaxReadMultipleFiles {
			return fmt.Errorf("read_multiple_files accepts at most %d paths; split the request", MaxReadMultipleFiles)
		}
		call.Paths[call.PathCount] = p
		call.PathCount++
	}
	if call.PathCount == 0 {
		return fmt.Errorf("read_multiple_files needs at least one path in paths")
	}
	return nil
}

// ReadPaths returns read_multiple_files' requested paths.
func ReadPaths(c Call) []string {
	n := min(max(c.PathCount, 0), MaxReadMultipleFiles)
	return append([]string(nil), c.Paths[:n]...)
}

// decodeReadMultipleBody accepts {"paths":[...]} or, as a small-model
// fallback, one path per line.
func decodeReadMultipleBody(call *Call) {
	body := strings.TrimSpace(call.Body)
	call.Body = ""
	if len(body) > maxFSToolPayloadBytes {
		call.InputErr = fmt.Sprintf("read_multiple_files arguments exceed the %d byte limit", maxFSToolPayloadBytes)
		return
	}
	var paths []string
	if strings.HasPrefix(body, "{") {
		var args readMultipleArgs
		if err := decodeOneJSONObject(body, &args); err != nil {
			call.InputErr = "read_multiple_files needs one JSON object {\"paths\":[...]} in the block body: " + err.Error()
			return
		}
		paths = args.Paths
	} else {
		paths = strings.Split(body, "\n")
	}
	if call.Path != "" {
		paths = append([]string{call.Path}, paths...)
		call.Path = ""
	}
	if err := setReadPaths(call, paths); err != nil {
		call.InputErr = err.Error()
	}
}

// decodeDirectoryTreeBody accepts an optional {"depth":N,"exclude":[...]}.
func decodeDirectoryTreeBody(call *Call) {
	body := strings.TrimSpace(call.Body)
	call.Body = ""
	if body == "" {
		return
	}
	if len(body) > maxFSToolPayloadBytes {
		call.InputErr = fmt.Sprintf("directory_tree arguments exceed the %d byte limit", maxFSToolPayloadBytes)
		return
	}
	var args directoryTreeArgs
	if err := decodeOneJSONObject(body, &args); err != nil {
		call.InputErr = "directory_tree accepts an optional JSON object {\"depth\":3,\"exclude\":[...]} in the block body: " + err.Error()
		return
	}
	if args.Path != "" {
		if call.Path != "" && filepathCleanSlash(call.Path) != filepathCleanSlash(args.Path) {
			call.InputErr = "directory_tree path is specified both in the fence and JSON body"
			return
		}
		call.Path = strings.TrimSpace(args.Path)
	}
	setDirectoryTreeOptions(call, args.Depth, args.Exclude)
}

func setDirectoryTreeOptions(call *Call, depth int, exclude []string) {
	if depth < 0 || depth > MaxTreeDepth {
		call.InputErr = fmt.Sprintf("directory_tree depth must be between 1 and %d", MaxTreeDepth)
		return
	}
	call.Max = depth
	if err := setGlobExcludes(call, exclude); err != nil {
		call.InputErr = strings.Replace(err.Error(), "glob", "directory_tree", 1)
	}
}

// readMultipleFiles reads each path through read_file's own confined
// reader. A failing path is reported inline and never aborts the others;
// the combined output is bounded by the runner's read cap.
func (r *Runner) readMultipleFiles(ctx context.Context, c Call) (string, ResultMeta, error) {
	meta := ResultMeta{Effect: EffectNone}
	paths := ReadPaths(c)
	if len(paths) == 0 {
		return "", meta, withCode(errors.New("read_multiple_files needs at least one path"), "invalid_arguments", RetryCorrectInput)
	}
	budget := r.maxKB * 1024
	var b strings.Builder
	var reasons []string
	failed := 0
	for i, p := range paths {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "==> %s <==\n", filepath.ToSlash(p))
		if budget <= 0 {
			b.WriteString("[skipped: combined output limit reached; read this file with read_file]\n")
			reasons = appendReason(reasons, "output_limit")
			continue
		}
		out, _, err := r.readFileMetaContextByte(ctx, p, 0, 0, nil)
		if err != nil {
			if ctx.Err() != nil {
				return "", meta, fmt.Errorf("read_multiple_files: %w", ctx.Err())
			}
			failed++
			fmt.Fprintf(&b, "[error: %s]\n", err.Error())
			reasons = appendReason(reasons, "file_errors")
			continue
		}
		if len(out) > budget {
			text, _ := boundedUTF8([]byte(out), budget)
			out = text + "\n[truncated: combined output limit reached; read the rest with read_file]"
			reasons = appendReason(reasons, "output_limit")
		}
		budget -= len(out)
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteString("\n")
		}
	}
	output := b.String()
	meta.Outcome = OutcomeOK
	meta.Coverage = Coverage{SourceComplete: len(reasons) == 0, CaptureComplete: true, PreviewComplete: len(reasons) == 0, ObservedBytes: int64(len(output)), RetainedBytes: int64(len(output)), Reasons: reasons}
	if len(reasons) > 0 {
		meta.Outcome = OutcomePartial
	}
	if failed == len(paths) {
		return output, meta, withCode(errors.New("read_multiple_files could not read any of the requested files:\n"+output), "not_found", RetryCorrectInput)
	}
	return output, meta, nil
}

func appendReason(reasons []string, reason string) []string {
	for _, r := range reasons {
		if r == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

// confinedRel resolves rel through Runner.resolve and returns the cleaned
// root-relative name for *os.Root calls ("." for the workspace itself).
func (r *Runner) confinedRel(rel string) (string, error) {
	abs, err := r.resolve(rel)
	if err != nil {
		return "", withCode(err, "safety_block", RetryCorrectInput)
	}
	name, err := filepath.Rel(r.root, abs)
	if err != nil {
		return "", withCode(err, "safety_block", RetryCorrectInput)
	}
	return name, nil
}

// getFileInfo reports one path's metadata from Lstat, so a symlink is
// described as itself and never followed. It reads no content.
func (r *Runner) getFileInfo(c Call) (string, ResultMeta, error) {
	meta := ResultMeta{Effect: EffectNone}
	name, err := r.confinedRel(c.Path)
	if err != nil {
		return "", meta, err
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", meta, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return "", meta, withCode(fmt.Errorf("get_file_info: %w", err), "not_found", RetryCorrectInput)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "path: %s\n", filepath.ToSlash(name))
	fmt.Fprintf(&b, "type: %s\n", fileTypeName(info.Mode()))
	if info.Mode().IsRegular() {
		fmt.Fprintf(&b, "size: %d bytes\n", info.Size())
	}
	fmt.Fprintf(&b, "permissions: %s (%04o)\n", info.Mode().Perm(), info.Mode().Perm())
	fmt.Fprintf(&b, "modified: %s\n", info.ModTime().UTC().Format(time.RFC3339))
	created, changed := fileTimes(info)
	if !created.IsZero() {
		fmt.Fprintf(&b, "created: %s\n", created.UTC().Format(time.RFC3339))
	}
	if !changed.IsZero() {
		fmt.Fprintf(&b, "changed: %s\n", changed.UTC().Format(time.RFC3339))
	}
	output := b.String()
	meta.Outcome = OutcomeOK
	meta.Coverage = Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, ObservedBytes: int64(len(output)), RetainedBytes: int64(len(output))}
	return output, meta, nil
}

func fileTypeName(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode.IsRegular():
		return "file"
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	default:
		return "other"
	}
}

// treeNode is one directory_tree entry. Children is nil for non-directories
// (omitted) and non-nil — possibly empty — for directories that were
// listed. Truncated marks a directory whose children were not (all) listed
// because of the depth or entry limit.
type treeNode struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"`
	Children  []*treeNode `json:"children,omitzero"`
	Truncated bool        `json:"truncated,omitempty"`
}

// directoryTree returns a bounded JSON tree of a workspace directory. It
// never follows symlinks (they are listed as type "symlink"), skips .git,
// and honors glob-style exclude patterns relative to the requested path.
func (r *Runner) directoryTree(ctx context.Context, c Call) (string, ResultMeta, error) {
	meta := ResultMeta{Effect: EffectNone}
	depth := c.Max
	if depth == 0 {
		depth = DefaultTreeDepth
	}
	if depth < 1 || depth > MaxTreeDepth {
		return "", meta, withCode(fmt.Errorf("directory_tree depth must be between 1 and %d", MaxTreeDepth), "invalid_arguments", RetryCorrectInput)
	}
	base, err := r.confinedRel(c.Path)
	if err != nil {
		return "", meta, err
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", meta, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(base)
	if err != nil {
		return "", meta, withCode(fmt.Errorf("directory_tree: %w", err), "not_found", RetryCorrectInput)
	}
	if !info.IsDir() {
		return "", meta, withCode(fmt.Errorf("%q is not a directory (use get_file_info or read_file)", filepath.ToSlash(base)), "invalid_arguments", RetryCorrectInput)
	}
	walker := treeWalker{ctx: ctx, fsys: root.FS(), matcher: newGlobMatcher("", c.Filter, nil)}
	top := &treeNode{Name: filepath.ToSlash(base), Type: "directory"}
	if err := walker.fill(top, filepath.ToSlash(base), "", depth); err != nil {
		if ctx.Err() != nil {
			return "", meta, fmt.Errorf("directory_tree: %w", ctx.Err())
		}
		return "", meta, fmt.Errorf("directory_tree: %w", err)
	}
	body, err := json.Marshal(top)
	if err != nil {
		return "", meta, fmt.Errorf("directory_tree: %w", err)
	}
	if len(body) > r.maxKB*1024 {
		return "", meta, withCode(fmt.Errorf("the tree exceeds the %d KB output limit; pass a smaller depth, a subdirectory, or exclude patterns", r.maxKB), "unsupported_content", RetryCorrectInput)
	}
	output := string(body)
	meta.Outcome = OutcomeOK
	meta.Coverage = Coverage{SourceComplete: !walker.truncated, CaptureComplete: true, PreviewComplete: true, ObservedBytes: int64(len(output)), RetainedBytes: int64(len(output))}
	if walker.truncated {
		meta.Outcome = OutcomePartial
		meta.Coverage.Reasons = []string{"depth_or_entry_limit"}
	}
	return output, meta, nil
}

type treeWalker struct {
	ctx       context.Context
	fsys      fs.FS
	matcher   globMatcher
	nodes     int
	truncated bool
}

// fill lists dir (root-relative name) into node. rel is the path relative
// to the requested base, used for exclude matching.
func (w *treeWalker) fill(node *treeNode, dir, rel string, depth int) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	entries, err := fs.ReadDir(w.fsys, dir)
	if err != nil {
		node.Truncated = true
		w.truncated = true
		return nil //nolint:nilerr // an unreadable subdirectory is reported as truncated, not fatal
	}
	children := make([]*treeNode, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		if entry.IsDir() && name == ".git" {
			continue
		}
		if skip, err := w.matcher.excluded(childRel); err != nil {
			return err
		} else if skip {
			continue
		}
		if depth <= 0 {
			// Below the depth limit: report that the directory has
			// content without listing it.
			node.Truncated = true
			w.truncated = true
			return nil
		}
		if w.nodes >= maxTreeNodes {
			node.Children = children
			node.Truncated = true
			w.truncated = true
			return nil
		}
		w.nodes++
		children = append(children, &treeNode{Name: name, Type: fileTypeName(entry.Type())})
		if entry.IsDir() {
			child := children[len(children)-1]
			if err := w.fill(child, joinTreePath(dir, name), childRel, depth-1); err != nil {
				return err
			}
		}
	}
	node.Children = children
	return nil
}

func joinTreePath(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}
