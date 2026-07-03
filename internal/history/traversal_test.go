package history

import (
	"strings"
	"testing"
)

// Session names come from user input (/history load <name>); they must not
// be able to escape the history directory.
func TestNamesCannotEscapeHistoryDir(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../outside", "../../etc/passwd", "a/b", ".hidden", "..", ""} {
		if _, err := Load(dir, name); err == nil || !strings.Contains(err.Error(), "invalid session name") {
			t.Errorf("Load(%q) should be rejected, got %v", name, err)
		}
		if _, err := Save(dir, name, Session{}); err == nil || !strings.Contains(err.Error(), "invalid session name") {
			t.Errorf("Save(%q) should be rejected, got %v", name, err)
		}
	}

	// Normal names still work.
	if _, err := Save(dir, "session-20260703-120000", Session{}); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if _, err := Load(dir, "session-20260703-120000"); err != nil {
		t.Fatalf("valid name failed to load: %v", err)
	}
}
