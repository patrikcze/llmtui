//go:build ((freebsd || linux || windows || darwin) && (amd64 || arm64)) || (linux && riscv64)

package ffi

import (
	"fmt"
	"os"
	"runtime"
	"sync"
)

// filename is the name or path to the libffi shared library.
var filename string

var libffiInit struct {
	once sync.Once
	err  error
}

func init() {
	if configured := os.Getenv("FFI_LIBRARY_PATH"); configured != "" {
		filename = configured
	}
	if len(filename) == 0 {
		switch runtime.GOOS {
		case "freebsd", "linux":
			filename = "libffi.so.8"
		case "windows":
			filename = "libffi-8.dll"
		case "darwin":
			filename = "libffi.8.dylib"
		}
	}
}

func ensureInit() {
	if err := EnsureAvailable(); err != nil {
		panic(err)
	}
}

// EnsureAvailable loads libffi on first use and reports failures without
// panicking. Applications can call it at a recoverable native boundary.
func EnsureAvailable() error {
	libffiInit.once.Do(func() {
		libffiInit.err = initialize()
	})
	return libffiInit.err
}

func initialize() error {
	libffi, err := Load(filename)
	if err != nil {
		return fmt.Errorf("ffi: load %q: %w", filename, err)
	}

	prepCif, err = libffi.Get("ffi_prep_cif")
	if err != nil {
		return fmt.Errorf("ffi: resolve ffi_prep_cif: %w", err)
	}

	prepCifVar, err = libffi.Get("ffi_prep_cif_var")
	if err != nil {
		return fmt.Errorf("ffi: resolve ffi_prep_cif_var: %w", err)
	}

	call, err = libffi.Get("ffi_call")
	if err != nil {
		return fmt.Errorf("ffi: resolve ffi_call: %w", err)
	}

	closureAlloc, err = libffi.Get("ffi_closure_alloc")
	if err != nil {
		return fmt.Errorf("ffi: resolve ffi_closure_alloc: %w", err)
	}

	closureFree, err = libffi.Get("ffi_closure_free")
	if err != nil {
		return fmt.Errorf("ffi: resolve ffi_closure_free: %w", err)
	}

	prepClosureLoc, err = libffi.Get("ffi_prep_closure_loc")
	if err != nil {
		return fmt.Errorf("ffi: resolve ffi_prep_closure_loc: %w", err)
	}

	getStructOffsets, err = libffi.Get("ffi_get_struct_offsets")
	if err != nil {
		return fmt.Errorf("ffi: resolve ffi_get_struct_offsets: %w", err)
	}

	// Because ffi_get_version and ffi_get_version_number just exist since libffi 3.5.0, we don't panic here.
	getVersion, _ = libffi.Get("ffi_get_version")

	getVersionNumber, _ = libffi.Get("ffi_get_version_number")
	return nil
}
