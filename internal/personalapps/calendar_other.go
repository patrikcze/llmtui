//go:build !darwin

package personalapps

import "time"

// CalendarBackendOptions is available on every platform so the composition
// root needs no build tags. Non-macOS builds have no EventKit implementation.
type CalendarBackendOptions struct {
	HelperPath string
	Timeout    time.Duration
}

// NewCalendarBackend returns nil outside macOS. This keeps normal chat and
// source builds fully functional when the optional native companion is absent.
func NewCalendarBackend(CalendarBackendOptions) CalendarBackend { return nil }
