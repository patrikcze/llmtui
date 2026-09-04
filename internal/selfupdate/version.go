package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version of the shape vMAJOR.MINOR.PATCH with an
// optional -PRERELEASE suffix, matching the tags the release workflow
// publishes (v1.2.3, v1.2.3-rc1). Build metadata (+...) is accepted and
// ignored.
//
// A small in-repo implementation is used deliberately rather than adding a
// dependency: the tag shape is fixed and narrow, and CLAUDE.md forbids
// drive-by dependency additions.
type Version struct {
	Major, Minor, Patch int
	Pre                 string // prerelease identifiers without the leading '-', "" if none
}

// devVersions are the -ldflags placeholders and git-describe fallbacks that
// must never be ordered against a real release.
var devVersions = map[string]bool{
	"":        true,
	"dev":     true,
	"none":    true,
	"unknown": true,
}

// IsDevBuild reports whether v names a development/source build rather than a
// released version. Anything that does not parse as vMAJOR.MINOR.PATCH is
// treated as a dev build (e.g. a `git describe` string like
// "v1.0.22-3-gabc123" resolves to a real Version; a bare commit hash does
// not).
func IsDevBuild(v string) bool {
	if devVersions[strings.TrimSpace(v)] {
		return true
	}
	_, err := ParseVersion(v)
	return err != nil
}

// ParseVersion parses a version string. A leading 'v' is optional.
// "v1.0.22-3-gabcdef" (git describe) parses to 1.0.22 with prerelease
// "3-gabcdef" so an untagged local build still compares as slightly newer
// than its base tag rather than failing outright.
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	raw = strings.TrimPrefix(raw, "v")
	if raw == "" {
		return Version{}, fmt.Errorf("empty version")
	}
	// Strip build metadata.
	if i := strings.IndexByte(raw, '+'); i >= 0 {
		raw = raw[:i]
	}
	core := raw
	pre := ""
	if i := strings.IndexByte(raw, '-'); i >= 0 {
		core = raw[:i]
		pre = raw[i+1:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
	}
	var v Version
	var err error
	if v.Major, err = parseNonNegative(parts[0]); err != nil {
		return Version{}, fmt.Errorf("version %q: major: %w", s, err)
	}
	if v.Minor, err = parseNonNegative(parts[1]); err != nil {
		return Version{}, fmt.Errorf("version %q: minor: %w", s, err)
	}
	if v.Patch, err = parseNonNegative(parts[2]); err != nil {
		return Version{}, fmt.Errorf("version %q: patch: %w", s, err)
	}
	v.Pre = pre
	return v, nil
}

func parseNonNegative(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("negative")
	}
	return n, nil
}

// IsPrerelease reports whether the version carries a prerelease suffix.
func (v Version) IsPrerelease() bool { return v.Pre != "" }

// String renders the canonical "vMAJOR.MINOR.PATCH[-PRE]" form.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare returns -1, 0 or +1 as v is less than, equal to, or greater than o,
// following the semantic-versioning precedence rules: numeric core first,
// then a version WITH a prerelease ranks below the same core WITHOUT one,
// then prerelease identifiers are compared dot-by-dot (numeric identifiers
// numerically, alphanumeric identifiers lexically, numeric < alphanumeric).
func (v Version) Compare(o Version) int {
	if c := cmpInt(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpInt(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpInt(v.Patch, o.Patch); c != 0 {
		return c
	}
	if v.Pre == o.Pre {
		return 0
	}
	if v.Pre == "" {
		return 1 // release > prerelease
	}
	if o.Pre == "" {
		return -1
	}
	return comparePrerelease(v.Pre, o.Pre)
}

func comparePrerelease(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if c := cmpInt(an, bn); c != 0 {
				return c
			}
		case aErr == nil: // numeric identifier < alphanumeric identifier
			return -1
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(as), len(bs))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
