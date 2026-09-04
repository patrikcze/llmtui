package selfupdate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	runtimemgr "github.com/patrikcze/llmtui/internal/runtime"
)

// InstallCurrentResult reports what `self install` did.
type InstallCurrentResult struct {
	Target        Target
	RuntimeCopied bool
	RuntimeNote   string
}

// InstallCurrent installs the currently running llmtui build into target,
// together with its executable-relative bundled llama.cpp runtime when one is
// present and verifies against the embedded pin. It never downloads a runtime
// — a bare binary install is valid for network-provider users, who are told
// how to add the runtime later.
func InstallCurrent(resolvedExe string, target Target, version string, progress io.Writer) (*InstallCurrentResult, error) {
	logf := func(format string, a ...any) {
		if progress != nil {
			fmt.Fprintf(progress, format+"\n", a...)
		}
	}
	if err := ensureWritablePrefix(target); err != nil {
		return nil, err
	}
	if err := ValidateBinary(resolvedExe, runtime.GOOS); err != nil {
		return nil, fmt.Errorf("running executable %s: %w", resolvedExe, err)
	}

	stage, err := os.MkdirTemp(target.Prefix, ".llmtui-stage-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.Chmod(stage, 0o755); err != nil {
		return nil, err
	}

	stagedBin := filepath.Join(stage, binaryName(runtime.GOOS))
	if err := copyFile(resolvedExe, stagedBin, 0o755); err != nil {
		return nil, fmt.Errorf("stage binary: %w", err)
	}

	payload := &Payload{Dir: stage, BinPath: stagedBin}
	res := &InstallCurrentResult{Target: target}

	if src, ok := bundledRuntimeDir(); ok {
		dst := filepath.Join(stage, "lib", "llmtui", "runtime")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := copyTree(src, dst); err != nil {
			return nil, fmt.Errorf("stage bundled runtime: %w", err)
		}
		payload.RuntimeDir = dst
		res.RuntimeCopied = true
		logf("Bundled runtime found at %s — installing it alongside the binary.", src)
	} else {
		res.RuntimeNote = "this build has no bundled llama.cpp runtime; run `llmtui runtime install` if you want embedded local inference"
		logf("No bundled runtime; installing the binary only.")
	}

	if err := payload.Install(target, InstallMeta{
		Version: version,
		By:      "self install",
	}, false); err != nil {
		return nil, err
	}
	return res, nil
}

// bundledRuntimeDir returns the executable-relative llama.cpp runtime
// directory if the running build ships one and it verifies against the
// embedded pin (runtime resolver tier 3).
func bundledRuntimeDir() (string, bool) {
	res, err := runtimemgr.Resolve(runtimemgr.ResolveConfig{})
	if err != nil {
		return "", false
	}
	if res.Tier == 3 && res.Verified {
		return res.Dir, true
	}
	return "", false
}
