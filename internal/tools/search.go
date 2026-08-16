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
func (r *Runner) globFiles(ctx context.Context, rel, pattern string) (string, error) {
	pattern, err := normalizeGlobPattern(pattern)
	if err != nil {
		return "", err
	}
	base, err := r.searchBase(rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(base)
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("glob path %q is not a directory", rel)
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
		return "", fmt.Errorf("glob: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Sprintf("no files matched %q", pattern), nil
	}
	sort.Strings(matches)
	if truncated {
		matches = append(matches, fmt.Sprintf("… results limited to %d files", maxSearchResults))
	}
	return strings.Join(matches, "\n"), nil
}

// grepFiles searches files directly with Go's regexp engine. Recursive
// searches skip likely secret files; an explicit secret file is instead
// handled by Runner.NeedsApproval, matching read_file's policy.
func (r *Runner) grepFiles(ctx context.Context, rel, pattern, fileGlob string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("grep needs a regular expression")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("grep pattern: %w", err)
	}
	if strings.TrimSpace(fileGlob) != "" {
		fileGlob, err = normalizeGlobPattern(fileGlob)
		if err != nil {
			return "", fmt.Errorf("grep file glob: %w", err)
		}
	}
	base, err := r.searchBase(rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(base)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
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
			return "", fmt.Errorf("grep: %w", err)
		}
	} else {
		files = []string{base}
	}
	sort.Strings(files)

	var matches []string
	bytesUsed := 0
	limit := r.maxKB * 1024
	truncated := false
	for _, filePath := range files {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("grep: %w", err)
		}
		info, err := os.Stat(filePath)
		if err != nil || info.IsDir() || info.Size() > int64(limit) {
			continue
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		workspaceRel, err := filepath.Rel(r.root, filePath)
		if err != nil {
			return "", fmt.Errorf("grep: %w", err)
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
	if len(matches) == 0 {
		if filesTruncated {
			return fmt.Sprintf("no matches for %q in the first %d eligible files", pattern, maxSearchFiles), nil
		}
		return fmt.Sprintf("no matches for %q", pattern), nil
	}
	if filesTruncated && !truncated {
		matches = append(matches, fmt.Sprintf("… search limited to the first %d eligible files", maxSearchFiles))
	}
	if truncated {
		matches = append(matches, fmt.Sprintf("… results limited to %d matches and %d KB", maxSearchResults, r.maxKB))
	}
	return strings.Join(matches, "\n"), nil
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
			return "", fmt.Errorf("glob pattern cannot contain ..")
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
