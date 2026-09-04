package selfupdate

import "strings"

// mergePathEntry ensures dir appears exactly once in a PATH-style list
// without removing, reordering, or rewriting any existing entry. A new entry
// is appended. Duplicate detection trims surrounding whitespace and, when
// caseInsensitive is set (Windows), compares case-insensitively.
//
// It is a pure function so the PATH-editing logic can be tested on every
// platform, independent of the Windows registry glue that calls it.
func mergePathEntry(current, dir string, sep string, caseInsensitive bool) (result string, changed bool) {
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimRight(s, `\/`)
		if caseInsensitive {
			s = strings.ToLower(s)
		}
		return s
	}
	want := norm(dir)
	if want == "" {
		return current, false
	}
	for _, entry := range strings.Split(current, sep) {
		if norm(entry) == want {
			return current, false
		}
	}
	if current == "" {
		return dir, true
	}
	return current + sep + dir, true
}
