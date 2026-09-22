package tools

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxSearchResults = 200
	maxSearchFiles   = 10_000
)

// globFiles recursively finds workspace files without invoking a shell. A
// pattern without a slash matches file names at any depth; ** is supported as
// a complete path segment for recursive path matching.
func (r *Runner) globFiles(ctx context.Context, rel, pattern string) (string, ResultMeta, error) {
	meta := ResultMeta{Effect: EffectNone}
	pattern, err := normalizeGlobPattern(pattern)
	if err != nil {
		return "", meta, withCode(err, "invalid_pattern", RetryCorrectInput)
	}
	base, err := r.searchBase(rel)
	if err != nil {
		return "", meta, withCode(err, "safety_block", RetryCorrectInput)
	}
	info, err := os.Stat(base)
	if err != nil {
		return "", meta, withCode(fmt.Errorf("glob: %w", err), "not_found", RetryCorrectInput)
	}
	if !info.IsDir() {
		return "", meta, withCode(fmt.Errorf("glob path %q is not a directory", rel), "invalid_arguments", RetryCorrectInput)
	}

	var matches []string
	truncated := false
	err = filepath.WalkDir(base, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		workspaceRel, err := filepath.Rel(r.root, filePath)
		if err != nil {
			return err
		}
		workspaceRel = filepath.ToSlash(workspaceRel)
		if entry.IsDir() {
			if filePath != base && isGitMetadataPath(workspaceRel) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		baseRel, err := filepath.Rel(base, filePath)
		if err != nil {
			return err
		}
		matched, err := matchSearchGlob(pattern, filepath.ToSlash(baseRel))
		if err != nil {
			return err
		}
		if !matched {
			return nil
		}
		if len(matches) >= maxSearchResults {
			truncated = true
			return fs.SkipAll
		}
		matches = append(matches, workspaceRel)
		return nil
	})
	if err != nil {
		return "", meta, fmt.Errorf("glob: %w", err)
	}
	// glob always reports OutcomeOK, matching list_dir: the cap is a
	// documented bound on an otherwise-complete scan, not a failed scan.
	meta.Outcome = OutcomeOK
	meta.Coverage = Coverage{SourceComplete: !truncated, CaptureComplete: !truncated, PreviewComplete: true, RetainedBytes: 0}
	if truncated {
		meta.Coverage.Reasons = []string{"files"}
	}
	if len(matches) == 0 {
		return fmt.Sprintf("no files matched %q", pattern), meta, nil
	}
	sort.Strings(matches)
	if truncated {
		matches = append(matches, fmt.Sprintf("… results limited to %d files", maxSearchResults))
	}
	joined := strings.Join(matches, "\n")
	meta.Coverage.ObservedBytes, meta.Coverage.RetainedBytes = int64(len(joined)), int64(len(joined))
	return joined, meta, nil
}

// grepFiles searches files directly with Go's regexp engine. Recursive
// searches skip likely secret files; an explicit secret file is instead
// handled by Runner.NeedsApproval, matching read_file's policy.
func (r *Runner) grepFiles(ctx context.Context, rel, pattern, fileGlob string) (string, ResultMeta, error) {
	meta := ResultMeta{Effect: EffectNone}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", meta, withCode(fmt.Errorf("grep needs a regular expression"), "invalid_arguments", RetryCorrectInput)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", meta, withCode(fmt.Errorf("grep pattern: %w", err), "invalid_pattern", RetryCorrectInput)
	}
	if strings.TrimSpace(fileGlob) != "" {
		fileGlob, err = normalizeGlobPattern(fileGlob)
		if err != nil {
			return "", meta, withCode(fmt.Errorf("grep file glob: %w", err), "invalid_pattern", RetryCorrectInput)
		}
	}
	base, err := r.searchBase(rel)
	if err != nil {
		return "", meta, withCode(err, "safety_block", RetryCorrectInput)
	}
	info, err := os.Stat(base)
	if err != nil {
		return "", meta, withCode(fmt.Errorf("grep: %w", err), "not_found", RetryCorrectInput)
	}
	recursive := info.IsDir()
	var files []string
	filesTruncated := false
	if recursive {
		err = filepath.WalkDir(base, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			workspaceRel, err := filepath.Rel(r.root, filePath)
			if err != nil {
				return err
			}
			workspaceRel = filepath.ToSlash(workspaceRel)
			if entry.IsDir() {
				if filePath != base && isGitMetadataPath(workspaceRel) {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || IsSecretPath(workspaceRel) {
				return nil
			}
			if fileGlob != "" {
				baseRel, err := filepath.Rel(base, filePath)
				if err != nil {
					return err
				}
				matched, err := matchSearchGlob(fileGlob, filepath.ToSlash(baseRel))
				if err != nil {
					return err
				}
				if !matched {
					return nil
				}
			}
			if len(files) >= maxSearchFiles {
				filesTruncated = true
				return fs.SkipAll
			}
			files = append(files, filePath)
			return nil
		})
		if err != nil {
			return "", meta, fmt.Errorf("grep: %w", err)
		}
	} else {
		files = []string{base}
	}
	sort.Strings(files)

	var matches []string
	bytesUsed := 0
	limit := r.maxKB * 1024
	truncated := false
	skippedLarge, skippedUnreadable, skippedBinary := false, false, false
	for _, filePath := range files {
		if err := ctx.Err(); err != nil {
			return "", meta, fmt.Errorf("grep: %w", err)
		}
		info, err := os.Stat(filePath)
		if err != nil {
			skippedUnreadable = true
			continue
		}
		if info.IsDir() {
			continue
		}
		if info.Size() > int64(limit) {
			skippedLarge = true
			continue
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			skippedUnreadable = true
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 {
			skippedBinary = true
			continue
		}
		workspaceRel, err := filepath.Rel(r.root, filePath)
		if err != nil {
			return "", meta, fmt.Errorf("grep: %w", err)
		}
		workspaceRel = filepath.ToSlash(workspaceRel)
		for index, line := range strings.Split(string(data), "\n") {
			if !re.MatchString(line) {
				continue
			}
			match := fmt.Sprintf("%s:%d:%s", workspaceRel, index+1, truncateLine(line, 500))
			if len(matches) >= maxSearchResults || bytesUsed+len(match)+1 > limit {
				truncated = true
				break
			}
			matches = append(matches, match)
			bytesUsed += len(match) + 1
		}
		if truncated {
			break
		}
	}
	// grep is OutcomeOK for an exhaustive scan (including zero matches — a
	// clean negative result is not a failure) and OutcomePartial whenever any
	// cap fired, so a capped zero-match result is never reported as an
	// exhaustive negative.
	complete := !filesTruncated && !truncated
	var reasons []string
	if filesTruncated {
		reasons = append(reasons, "files")
	}
	if truncated {
		reasons = append(reasons, "matches")
	}
	if skippedLarge {
		reasons = append(reasons, "large")
	}
	if skippedUnreadable {
		reasons = append(reasons, "unreadable")
	}
	if skippedBinary {
		reasons = append(reasons, "binary")
	}
	if skippedLarge || skippedUnreadable || skippedBinary {
		complete = false
	}
	outcome := OutcomeOK
	if !complete {
		outcome = OutcomePartial
	}
	meta.Outcome = outcome
	meta.Coverage = Coverage{SourceComplete: complete, CaptureComplete: complete, PreviewComplete: true, Reasons: reasons}
	if len(matches) == 0 {
		meta.Coverage.ObservedBytes = 0
		if filesTruncated {
			return fmt.Sprintf("no matches for %q in the first %d eligible files", pattern, maxSearchFiles), meta, nil
		}
		return fmt.Sprintf("no matches for %q", pattern), meta, nil
	}
	if filesTruncated && !truncated {
		matches = append(matches, fmt.Sprintf("… search limited to the first %d eligible files", maxSearchFiles))
	}
	if truncated {
		matches = append(matches, fmt.Sprintf("… results limited to %d matches and %d KB", maxSearchResults, r.maxKB))
	}
	joined := strings.Join(matches, "\n")
	meta.Coverage.ObservedBytes, meta.Coverage.RetainedBytes = int64(bytesUsed), int64(len(joined))
	return joined, meta, nil
}

func (r *Runner) searchBase(rel string) (string, error) {
	base, err := r.resolve(rel)
	if err != nil {
		return "", err
	}
	workspaceRel, err := filepath.Rel(r.root, base)
	if err != nil {
		return "", err
	}
	if isGitMetadataPath(filepath.ToSlash(workspaceRel)) {
		return "", fmt.Errorf("searching .git metadata is not allowed")
	}
	return base, nil
}

func isGitMetadataPath(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	return rel == ".git" || strings.HasPrefix(rel, ".git/")
}

func normalizeGlobPattern(pattern string) (string, error) {
	pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	if pattern == "" {
		return "", fmt.Errorf("glob needs a pattern")
	}
	if strings.HasPrefix(pattern, "/") {
		return "", fmt.Errorf("glob pattern must be relative to the search path")
	}
	segments := strings.Split(pattern, "/")
	for _, segment := range segments {
		if segment == ".." {
			return "", fmt.Errorf("glob pattern cannot contain parent-directory segments")
		}
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return "", fmt.Errorf("invalid glob pattern: %w", err)
		}
	}
	return pattern, nil
}

func matchSearchGlob(pattern, name string) (bool, error) {
	pattern = strings.TrimPrefix(pattern, "./")
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if !strings.Contains(pattern, "/") {
		return path.Match(pattern, path.Base(name))
	}
	return matchGlobSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchGlobSegments(pattern, name []string) (bool, error) {
	if len(pattern) == 0 {
		return len(name) == 0, nil
	}
	if pattern[0] == "**" {
		matched, err := matchGlobSegments(pattern[1:], name)
		if err != nil || matched {
			return matched, err
		}
		if len(name) == 0 {
			return false, nil
		}
		return matchGlobSegments(pattern, name[1:])
	}
	if len(name) == 0 {
		return false, nil
	}
	matched, err := path.Match(pattern[0], name[0])
	if err != nil || !matched {
		return false, err
	}
	return matchGlobSegments(pattern[1:], name[1:])
}
