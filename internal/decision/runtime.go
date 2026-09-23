package decision

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	RuntimeFormatSafeTensors = "safetensors"
	RuntimeFormatONNX        = "onnx"
	RuntimeFormatMLX         = "mlx"
)

var ErrRuntimeArtifactUnavailable = errors.New("verified decision runtime artifact unavailable")

// RuntimeLoader is the narrow boundary for a verified executable model. The
// loader owns backend details (for example ONNX Runtime or a Python MLX
// worker). Loading never downloads models or installs runtime dependencies.
type RuntimeLoader interface {
	Load(ctx context.Context, installation Installation) (Engine, error)
}

// SourceRuntimeLoader explicitly opts a backend into executing a verified
// source bundle. Ready ONNX artifacts retain their independent validation.
type SourceRuntimeLoader interface {
	RuntimeLoader
	SupportsSource(format string) bool
}

// LoadRuntime validates the installation again immediately before handing it
// to a backend. A source-only SafeTensors checkpoint returns an explicit
// unavailable engine and never reaches a loader.
func LoadRuntime(ctx context.Context, installation Installation, loader RuntimeLoader) (Engine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if loader == nil {
		return UnavailableEngine{Reason: "no runtime loader configured"}, ErrRuntimeArtifactUnavailable
	}
	if !installation.Valid {
		return UnavailableEngine{Reason: "model installation is not valid"}, fmt.Errorf("%w: invalid installation", ErrRuntimeArtifactUnavailable)
	}
	if err := validateRuntimeArtifact(installation.Manifest.Runtime); err != nil {
		return UnavailableEngine{Reason: err.Error()}, fmt.Errorf("%w: %v", ErrRuntimeArtifactUnavailable, err)
	}
	if !installation.Manifest.Runtime.Ready {
		source, ok := loader.(SourceRuntimeLoader)
		if !ok || !source.SupportsSource(installation.Manifest.Runtime.Format) || installation.Manifest.Runtime.Format != RuntimeFormatMLX {
			return UnavailableEngine{Reason: installation.Manifest.Runtime.Note}, ErrRuntimeArtifactUnavailable
		}
		if err := verifyMLXInstallation(ctx, installation); err != nil {
			return nil, err
		}
	} else {
		path, err := RuntimeArtifactPath(installation)
		if err != nil {
			return UnavailableEngine{Reason: err.Error()}, err
		}
		if err := verifyFile(path, installation.Manifest.Runtime.Size, installation.Manifest.Runtime.SHA256); err != nil {
			return UnavailableEngine{Reason: err.Error()}, fmt.Errorf("%w: verify runtime artifact: %v", ErrRuntimeArtifactUnavailable, err)
		}
	}
	var engine Engine
	var err error
	// A package-owned loader may skip its redundant public-boundary check.
	if verified, ok := loader.(interface {
		loadVerified(context.Context, Installation) (Engine, error)
	}); ok {
		engine, err = verified.loadVerified(ctx, installation)
	} else {
		engine, err = loader.Load(ctx, installation)
	}
	if err != nil {
		return nil, fmt.Errorf("load decision runtime: %w", err)
	}
	if engine == nil {
		return nil, errors.New("load decision runtime: loader returned a nil engine")
	}
	return engine, nil
}

func validateRuntimeArtifact(runtime RuntimeArtifact) error {
	if runtime.Ready {
		if runtime.Format != RuntimeFormatONNX {
			return fmt.Errorf("ready runtime format %q is not supported", runtime.Format)
		}
		if !safeRelativePath(runtime.Path) {
			return fmt.Errorf("ready runtime path %q is unsafe", runtime.Path)
		}
		if runtime.Size <= 0 {
			return errors.New("ready runtime size must be positive")
		}
		if !isSHA256(runtime.SHA256) {
			return errors.New("ready runtime sha256 must be 64 hexadecimal characters")
		}
		if strings.TrimSpace(runtime.Exporter) == "" {
			return errors.New("ready runtime exporter is required")
		}
		if strings.TrimSpace(runtime.UpstreamRevision) == "" {
			return errors.New("ready runtime upstream revision is required")
		}
		return nil
	}
	if runtime.Format != RuntimeFormatSafeTensors && runtime.Format != RuntimeFormatMLX {
		return fmt.Errorf("source-only runtime format %q is not supported", runtime.Format)
	}
	if runtime.Path != "" || runtime.SHA256 != "" || runtime.Exporter != "" || runtime.UpstreamRevision != "" {
		return errors.New("source-only runtime must not contain executable artifact metadata")
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != hex.EncodedLen(sha256Size) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

const sha256Size = 32

// RuntimeArtifactPath returns the installed executable path after validating
// the manifest metadata. It is intended for a backend loader, not callers
// that bypass LoadRuntime.
func RuntimeArtifactPath(installation Installation) (string, error) {
	if !installation.Valid || !installation.Manifest.Runtime.Ready {
		return "", ErrRuntimeArtifactUnavailable
	}
	if err := validateRuntimeArtifact(installation.Manifest.Runtime); err != nil {
		return "", err
	}
	path, err := safeJoin(installation.Path, installation.Manifest.Runtime.Path)
	if err != nil {
		return "", err
	}
	if err := rejectSymlinkChain(path, installation.Path); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("runtime artifact %s is not installed: %w", filepath.ToSlash(installation.Manifest.Runtime.Path), ErrRuntimeArtifactUnavailable)
}
