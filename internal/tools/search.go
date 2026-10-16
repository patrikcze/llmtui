package tools

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

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

// MaxGlobExcludes caps glob's exclude list so one call cannot smuggle an
// unbounded matcher set into every directory visited.
const MaxGlobExcludes = 16

// globExcludes splits glob's newline-joined exclude list. It is carried in
// Call.Filter (grep's file filter; glob otherwise leaves it empty) so the
// call stays comparable and its cursor and no-progress identity cover the
// excludes without another field.
func globExcludes(filter string) []string {
	var out []string
	for _, line := range strings.Split(filter, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// setGlobExcludes validates and stores glob's exclude patterns on call.
func setGlobExcludes(call *Call, excludes []string) error {
	if len(excludes) > MaxGlobExcludes {
		return fmt.Errorf("glob accepts at most %d exclude patterns", MaxGlobExcludes)
	}
	normalized := make([]string, 0, len(excludes))
	for _, pattern := range excludes {
		if strings.TrimSpace(pattern) == "" {
			continue
		}
		clean, err := normalizeGlobPattern(pattern)
		if err != nil {
			return fmt.Errorf("exclude %q: %w", pattern, err)
		}
		normalized = append(normalized, clean)
	}
	call.Filter = strings.Join(normalized, "\n")
	return nil
}

// globMatcher applies glob's include pattern and exclude list under one case
// policy. An exclude matches a path relative to the search base or, for a
// pattern without "/", any single name — so "node_modules" prunes that
// directory wherever it appears.
type globMatcher struct {
	pattern  string
	excludes []string
	fold     bool
}

func newGlobMatcher(pattern, filter string, caseSensitive *bool) globMatcher {
	m := globMatcher{pattern: pattern, excludes: globExcludes(filter), fold: caseSensitive != nil && !*caseSensitive}
	if m.fold {
		m.pattern = strings.ToLower(m.pattern)
		for i := range m.excludes {
			m.excludes[i] = strings.ToLower(m.excludes[i])
		}
	}
	return m
}

func (m globMatcher) key(name string) string {
	if m.fold {
		return strings.ToLower(name)
	}
	return name
}

func (m globMatcher) included(rel string) (bool, error) {
	return matchSearchGlob(m.pattern, m.key(rel))
}

func (m globMatcher) excluded(rel string) (bool, error) {
	rel = m.key(rel)
	for _, pattern := range m.excludes {
		matched, err := matchSearchGlob(pattern, rel)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}
