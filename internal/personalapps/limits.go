package personalapps

import "time"

// Limits bounds every request this package accepts. They are starting
// targets from the integration plan, to be tuned once the native adapters
// are measured; they are not performance promises. The composition root maps
// configuration onto them, and a zero field takes the package default so a
// partially configured Limits can never silently mean "unbounded".
type Limits struct {
	// ReadTimeout bounds one read operation end to end.
	ReadTimeout time.Duration
	// MutationTimeout bounds one apply operation end to end. Exceeding it
	// yields an unknown outcome, never a safe retry.
	MutationTimeout time.Duration
	// PageSize is the page size used when a request omits one.
	PageSize int
	// MaxPageSize is the largest page a request may ask for.
	MaxPageSize int
	// MaxMessagesPerRead bounds one mail_read call.
	MaxMessagesPerRead int
	// MaxBodyBytes bounds the body returned per selected message.
	MaxBodyBytes int
	// MaxResultBytes bounds one serialized model-facing result.
	MaxResultBytes int
	// MaxScanCandidates bounds how many candidates a search may examine
	// before it reports incomplete coverage.
	MaxScanCandidates int
	// MaxCalendarDays bounds a calendar query window.
	MaxCalendarDays int
	// MaxChangesPerPlan bounds one prepared plan.
	MaxChangesPerPlan int
	// MaxRequestBytes bounds the raw request payload.
	MaxRequestBytes int
	// MaxRequestDepth bounds JSON nesting in a request.
	MaxRequestDepth int
	// MaxStringBytes bounds any single string field in a request.
	MaxStringBytes int
	// PlanTTL is how long a prepared plan stays applicable.
	PlanTTL time.Duration
	// MaxActivePlans bounds simultaneously prepared plans.
	MaxActivePlans int
}

// Package defaults. They mirror the documented configuration defaults so an
// unconfigured Service is already bounded.
const (
	defaultReadTimeout        = 15 * time.Second
	defaultMutationTimeout    = 30 * time.Second
	defaultPageSize           = 25
	defaultMaxPageSize        = 100
	defaultMaxMessagesPerRead = 10
	defaultMaxBodyBytes       = 32 * 1024
	defaultMaxResultBytes     = 128 * 1024
	defaultMaxScanCandidates  = 1000
	defaultMaxCalendarDays    = 31
	defaultMaxChangesPerPlan  = 25
	defaultMaxRequestBytes    = 256 * 1024
	defaultMaxRequestDepth    = 12
	defaultMaxStringBytes     = 4096
	defaultPlanTTL            = 5 * time.Minute
	defaultMaxActivePlans     = 8
)

// DefaultLimits returns the documented starting limits.
func DefaultLimits() Limits {
	return Limits{
		ReadTimeout:        defaultReadTimeout,
		MutationTimeout:    defaultMutationTimeout,
		PageSize:           defaultPageSize,
		MaxPageSize:        defaultMaxPageSize,
		MaxMessagesPerRead: defaultMaxMessagesPerRead,
		MaxBodyBytes:       defaultMaxBodyBytes,
		MaxResultBytes:     defaultMaxResultBytes,
		MaxScanCandidates:  defaultMaxScanCandidates,
		MaxCalendarDays:    defaultMaxCalendarDays,
		MaxChangesPerPlan:  defaultMaxChangesPerPlan,
		MaxRequestBytes:    defaultMaxRequestBytes,
		MaxRequestDepth:    defaultMaxRequestDepth,
		MaxStringBytes:     defaultMaxStringBytes,
		PlanTTL:            defaultPlanTTL,
		MaxActivePlans:     defaultMaxActivePlans,
	}
}

// withDefaults fills every unset field from DefaultLimits.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.ReadTimeout <= 0 {
		l.ReadTimeout = d.ReadTimeout
	}
	if l.MutationTimeout <= 0 {
		l.MutationTimeout = d.MutationTimeout
	}
	if l.PageSize <= 0 {
		l.PageSize = d.PageSize
	}
	if l.MaxPageSize <= 0 {
		l.MaxPageSize = d.MaxPageSize
	}
	if l.MaxMessagesPerRead <= 0 {
		l.MaxMessagesPerRead = d.MaxMessagesPerRead
	}
	if l.MaxBodyBytes <= 0 {
		l.MaxBodyBytes = d.MaxBodyBytes
	}
	if l.MaxResultBytes <= 0 {
		l.MaxResultBytes = d.MaxResultBytes
	}
	if l.MaxScanCandidates <= 0 {
		l.MaxScanCandidates = d.MaxScanCandidates
	}
	if l.MaxCalendarDays <= 0 {
		l.MaxCalendarDays = d.MaxCalendarDays
	}
	if l.MaxChangesPerPlan <= 0 {
		l.MaxChangesPerPlan = d.MaxChangesPerPlan
	}
	if l.MaxRequestBytes <= 0 {
		l.MaxRequestBytes = d.MaxRequestBytes
	}
	if l.MaxRequestDepth <= 0 {
		l.MaxRequestDepth = d.MaxRequestDepth
	}
	if l.MaxStringBytes <= 0 {
		l.MaxStringBytes = d.MaxStringBytes
	}
	if l.PlanTTL <= 0 {
		l.PlanTTL = d.PlanTTL
	}
	if l.MaxActivePlans <= 0 {
		l.MaxActivePlans = d.MaxActivePlans
	}
	if l.PageSize > l.MaxPageSize {
		l.PageSize = l.MaxPageSize
	}
	return l
}

// Validate rejects configured limits that are internally inconsistent or
// large enough to defeat their own purpose. It is called before any adapter
// exists, so an invalid configuration fails without launching an app.
func (l Limits) Validate() error {
	full := l.withDefaults()
	switch {
	case l.PageSize > 0 && l.MaxPageSize > 0 && l.PageSize > l.MaxPageSize:
		return Errorf(CodeInvalidRequest, "page_size %d exceeds max_page_size %d", l.PageSize, l.MaxPageSize)
	case full.MaxBodyBytes > full.MaxResultBytes:
		return Errorf(CodeInvalidRequest, "max_body_bytes %d exceeds max_result_bytes %d", full.MaxBodyBytes, full.MaxResultBytes)
	case full.MaxMessagesPerRead > full.MaxPageSize:
		return Errorf(CodeInvalidRequest, "max_messages_per_read %d exceeds max_page_size %d", full.MaxMessagesPerRead, full.MaxPageSize)
	case full.MaxScanCandidates < full.MaxPageSize:
		return Errorf(CodeInvalidRequest, "max_scan_candidates %d is below max_page_size %d", full.MaxScanCandidates, full.MaxPageSize)
	case full.MaxStringBytes > full.MaxRequestBytes:
		return Errorf(CodeInvalidRequest, "max_string_bytes %d exceeds max_request_bytes %d", full.MaxStringBytes, full.MaxRequestBytes)
	case full.MaxRequestDepth > 64:
		return Errorf(CodeInvalidRequest, "max_request_depth %d is unreasonably deep", full.MaxRequestDepth)
	}
	return nil
}
