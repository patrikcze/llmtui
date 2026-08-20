package llamart

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/hybridgroup/yzma/pkg/loader"
)

func validateRequiredLibraries(dir string) error {
	for _, pattern := range requiredLibraryPatterns() {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return fmt.Errorf("validate runtime library pattern %q: %w", pattern, err)
		}
		found := false
		for _, match := range matches {
			if info, statErr := os.Stat(match); statErr == nil && !info.IsDir() {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("required llama.cpp runtime library matching %q not found in %q", pattern, dir)
		}
	}
	return nil
}

func requiredLibraryPatterns() []string {
	switch runtime.GOOS {
	case "darwin":
		patterns := []string{
			"libllama*.dylib",
			"libggml*.dylib",
			"libggml-base*.dylib",
			"libggml-cpu*.dylib",
			"libggml-blas*.dylib",
		}
		if runtime.GOARCH == "arm64" {
			patterns = append(patterns, "libggml-metal*.dylib")
		}
		return patterns
	case "linux":
		return []string{"libllama*.so*", "libggml.so*", "libggml-base.so*", "libggml-cpu*.so*"}
	case "windows":
		return []string{"llama.dll", "ggml.dll", "ggml-base.dll", "ggml-cpu*.dll"}
	default:
		return []string{filepath.Base(loader.GetLibraryFilename(".", "llama"))}
	}
}
