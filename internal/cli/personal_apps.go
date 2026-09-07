package cli

import (
	"fmt"
	"runtime"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/personalapps"
)

// personalAppsDoctorLines reports configuration and companion readiness
// without launching Mail, Calendar or the EventKit companion. TCC access is
// intentionally not probed here: only a person connecting Calendar may cause
// macOS to show the full-access prompt.
func personalAppsDoctorLines(cfg config.PersonalAppsConfig) []string {
	return personalAppsDoctorLinesFor(runtime.GOOS, cfg, personalapps.CheckCalendarHelper)
}

func personalAppsDoctorLinesFor(platform string, cfg config.PersonalAppsConfig, checkCalendar func(string) personalapps.CalendarHelperStatus) []string {
	if !cfg.Enabled {
		return []string{"✗ personal apps disabled (personal_apps.enabled)"}
	}
	lines := []string{"✓ personal apps enabled"}
	if platform != "darwin" {
		return append(lines, "✗ Apple Mail and Calendar integration requires macOS")
	}
	if cfg.Mail.Enabled {
		if len(cfg.Mail.AllowedAccounts) == 0 {
			lines = append(lines, "✗ Mail enabled but no account UUID is allowed")
		} else {
			lines = append(lines, fmt.Sprintf("✓ Mail enabled with %d allowed account(s); connect it explicitly in chat", len(cfg.Mail.AllowedAccounts)))
		}
	} else {
		lines = append(lines, "✗ Mail disabled (personal_apps.mail.enabled)")
	}
	if !cfg.Calendar.Enabled {
		return append(lines, "✗ Calendar disabled (personal_apps.calendar.enabled)")
	}
	status := checkCalendar(cfg.Calendar.HelperPath)
	prefix := "✗ "
	if status.Ready {
		prefix = "✓ "
	}
	lines = append(lines, prefix+"Calendar: "+status.Message)
	if len(cfg.Calendar.AllowedCalendars) == 0 {
		lines = append(lines, "✗ Calendar enabled but no calendar identifier is allowed")
	} else {
		lines = append(lines, fmt.Sprintf("✓ Calendar enabled with %d allowed calendar(s); connect it explicitly in chat", len(cfg.Calendar.AllowedCalendars)))
	}
	lines = append(lines, "  Calendar permission is not probed by doctor; a denied or revoked grant is reported when an explicitly connected calendar operation runs")
	return lines
}
