//go:build !darwin

package personalapps

import "time"

// MailBackendOptions configures the real Mail adapter. The type exists on
// every platform so callers do not need a build-tagged call site; only the
// darwin build supplies a working adapter behind it.
type MailBackendOptions struct {
	Timeout time.Duration
}

// NewMailBackend returns nil on every platform but macOS: there is no Mail
// automation surface to bridge to here. personalapps.Service already treats
// a nil MailBackend as "adapter absent" and reports
// ErrUnsupportedPlatform for every mail operation, exactly as it does today
// before any real adapter exists.
func NewMailBackend(MailBackendOptions) MailBackend { return nil }
