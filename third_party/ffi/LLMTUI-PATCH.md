# Local patch to jupiterrider/ffi

This directory is upstream `github.com/jupiterrider/ffi` **v0.8.0**
(without `examples/`, `.github/` and `*_test.go`), used through the
`replace github.com/jupiterrider/ffi => ./third_party/ffi` line in llmtui's
`go.mod`. llmtui changes only `init.go` and `ffi.go`:

- **Lazy loading.** Upstream loads libffi in `init()` and panics when it is
  missing, which crashed llmtui at startup on Linux without `libffi.so.8`
  for every provider, not only embedded. Here `init()` only picks the file
  name; libffi is loaded on first use behind a `sync.Once`.
- **`EnsureAvailable() error`** loads libffi and reports a failure as an
  error. llmtui calls it before the first native call
  (`internal/provider/embedded/llamart/runtime.go`), so a missing libffi is an
  embedded-provider error instead of a panic.
- **`ensureInit()`** at the top of every exported function in `ffi.go` that
  calls into libffi, for callers that skip `EnsureAvailable`.
- **`FFI_LIBRARY_PATH`** overrides the libffi file name or path.

To upgrade: copy the new upstream release over this directory with the same
exclusions, keep this file and `init.go`, and re-add `ensureInit()` to each
exported libffi-calling function in `ffi.go` (`diff` against the pristine
upstream `ffi.go` shows exactly those lines). Then update the version above
and in `go.mod`.
