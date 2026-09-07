package personalapps

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CalendarHelperStatus is the result of a passive companion preflight. It
// never starts the companion, opens EventKit, requests a TCC permission, or
// accesses calendar content. A ready helper still needs an explicit Calendar
// connection and macOS full-calendar access when a real operation runs.
type CalendarHelperStatus struct {
	Ready   bool
	Code    string
	Message string
}

// CheckCalendarHelper reports whether the configured companion can be
// launched on this platform. It is deliberately useful to doctor commands and
// configuration UIs before a person chooses to connect Calendar.
func CheckCalendarHelper(path string) CalendarHelperStatus {
	return checkCalendarHelper(runtime.GOOS, path, func(name string) (os.FileMode, error) {
		info, err := os.Stat(name)
		if err != nil {
			return 0, err
		}
		return info.Mode(), nil
	})
}

func checkCalendarHelper(platform, path string, stat func(string) (os.FileMode, error)) CalendarHelperStatus {
	if platform != "darwin" {
		return CalendarHelperStatus{Code: "unsupported_platform", Message: "Calendar requires macOS and the EventKit companion"}
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return CalendarHelperStatus{Code: "missing_path", Message: "calendar.helper_path is not configured"}
	}
	if !filepath.IsAbs(path) {
		return CalendarHelperStatus{Code: "relative_path", Message: "calendar.helper_path must be an absolute executable path"}
	}
	mode, err := stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CalendarHelperStatus{Code: "missing_helper", Message: "the configured calendar helper does not exist"}
		}
		return CalendarHelperStatus{Code: "unavailable_helper", Message: "the configured calendar helper cannot be inspected"}
	}
	if !mode.IsRegular() {
		return CalendarHelperStatus{Code: "invalid_helper", Message: "the configured calendar helper is not a regular executable file"}
	}
	if mode.Perm()&0o111 == 0 {
		return CalendarHelperStatus{Code: "not_executable", Message: "the configured calendar helper is not executable"}
	}
	return CalendarHelperStatus{Ready: true, Code: "ready", Message: "the configured calendar helper is executable"}
}
