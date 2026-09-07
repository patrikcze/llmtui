//go:build darwin

package personalapps

import "testing"

func TestNewCalendarBackendRequiresAbsoluteCompanionPath(t *testing.T) {
	for _, path := range []string{"", "llmtui-personal-apps-calendar"} {
		if backend := NewCalendarBackend(CalendarBackendOptions{HelperPath: path}); backend != nil {
			t.Fatalf("NewCalendarBackend(%q) = %T, want nil", path, backend)
		}
	}
	if backend := NewCalendarBackend(CalendarBackendOptions{HelperPath: "/private/tmp/llmtui-personal-apps-calendar"}); backend == nil {
		t.Fatal("NewCalendarBackend returned nil for an absolute companion path")
	}
}
