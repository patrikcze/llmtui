package embedded

import "context"

// UnavailableRuntime is a placeholder Runtime for builds that do not wire in
// a native llama.cpp backend yet. It is temporary: a later stage replaces
// the constructor call in internal/app/factory.go with a real llama.cpp
// backed Runtime. Every method fails clearly instead of silently pretending
// to work, so HealthCheck and Chat surface an actionable error rather than a
// confusing hang or panic.
type UnavailableRuntime struct{}

// NewUnavailableRuntime returns a Runtime that always reports itself as
// unavailable. Exported so the factory (and tests) can construct it
// explicitly until a real runtime lands.
func NewUnavailableRuntime() *UnavailableRuntime { return &UnavailableRuntime{} }

// Probe always reports the runtime as unavailable.
func (UnavailableRuntime) Probe(Options) error { return errUnavailable }

// Load always fails; the embedded provider is not yet backed by a native
// inference engine in this build.
func (UnavailableRuntime) Load(context.Context, Options, func(string)) (ModelMeta, error) {
	return ModelMeta{}, errUnavailable
}

// Generate always fails.
func (UnavailableRuntime) Generate(context.Context, GenRequest, func(GenDelta)) (GenResult, error) {
	return GenResult{}, errUnavailable
}

// Close is a no-op; there is nothing to release.
func (UnavailableRuntime) Close() error { return nil }

var errUnavailable = wrapUnavailable("embedded inference runtime is not wired into this build yet")

func wrapUnavailable(msg string) error {
	return &unavailableError{msg: msg}
}

type unavailableError struct{ msg string }

func (e *unavailableError) Error() string { return e.msg }
func (e *unavailableError) Unwrap() error { return ErrRuntimeUnavailable }
