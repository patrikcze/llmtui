//go:build darwin

package personalapps

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveEventKitCalendarBridgeSmoke verifies the real, read-only companion
// against the signed-in macOS EventKit store. It is deliberately opt-in: the
// normal suite must never trigger a calendar permission prompt or enumerate
// a developer's calendars. It creates, updates, and deletes nothing.
func TestLiveEventKitCalendarBridgeSmoke(t *testing.T) {
	if os.Getenv("LLMTUI_TEST_LIVE_CALENDAR") != "1" {
		t.Skip("set LLMTUI_TEST_LIVE_CALENDAR=1 and LLMTUI_TEST_CALENDAR_HELPER=/absolute/path/to/helper to run against EventKit")
	}
	helper := os.Getenv("LLMTUI_TEST_CALENDAR_HELPER")
	backend := NewCalendarBackend(CalendarBackendOptions{HelperPath: helper, Timeout: 30 * time.Second})
	if backend == nil {
		t.Fatal("NewCalendarBackend returned nil; LLMTUI_TEST_CALENDAR_HELPER must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	calendars, err := backend.Calendars(ctx)
	if err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	if len(calendars) == 0 {
		t.Fatal("Calendars returned no EventKit calendars")
	}
	for _, calendar := range calendars {
		if calendar.Ref.Kind != KindCalendar || calendar.Ref.Adapter != AdapterCalendar || calendar.Ref.NativeID == "" {
			t.Fatalf("calendar reference = %+v, want an EventKit calendar identifier", calendar.Ref)
		}
	}
}
