// Package clipboard reads images from the system clipboard using platform
// tools, avoiding cgo so the binary stays trivially cross-compilable.
package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// ErrNoImage is returned when the clipboard holds no image data.
var ErrNoImage = errors.New("no image on the clipboard")

const readTimeout = 5 * time.Second

// ReadImage returns PNG data from the system clipboard.
func ReadImage(ctx context.Context) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	var (
		data []byte
		err  error
	)
	switch runtime.GOOS {
	case "darwin":
		data, err = readDarwin(ctx)
	case "linux":
		data, err = readLinux(ctx)
	case "windows":
		data, err = readWindows(ctx)
	default:
		return nil, "", fmt.Errorf("clipboard images are not supported on %s", runtime.GOOS)
	}
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", ErrNoImage
	}
	return data, "image/png", nil
}

func readDarwin(ctx context.Context) ([]byte, error) {
	// pngpaste is the fastest path when installed.
	if _, err := exec.LookPath("pngpaste"); err == nil {
		out, err := exec.CommandContext(ctx, "pngpaste", "-").Output()
		if err == nil && len(out) > 0 {
			return out, nil
		}
	}
	// Fallback: AppleScript writes the clipboard PNG to a temp file.
	tmp, err := os.CreateTemp("", "llmtui-paste-*.png")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	script := fmt.Sprintf(`set f to open for access POSIX file %q with write permission
set eof f to 0
try
	write (the clipboard as «class PNGf») to f
	close access f
on error
	close access f
	error "no image"
end try`, path)
	if err := exec.CommandContext(ctx, "osascript", "-e", script).Run(); err != nil {
		return nil, ErrNoImage
	}
	return os.ReadFile(path)
}

func readLinux(ctx context.Context) ([]byte, error) {
	type tool struct {
		name string
		args []string
	}
	tools := []tool{
		{"wl-paste", []string{"--type", "image/png"}},
		{"xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}},
	}
	for _, t := range tools {
		if _, err := exec.LookPath(t.name); err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, t.name, t.args...).Output()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			return out, nil
		}
	}
	return nil, errors.New("no clipboard image (needs wl-paste or xclip installed)")
}

func readWindows(ctx context.Context) ([]byte, error) {
	tmp, err := os.CreateTemp("", "llmtui-paste-*.png")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	script := fmt.Sprintf(`$img = Get-Clipboard -Format Image
if ($img -eq $null) { exit 1 }
$img.Save(%q, [System.Drawing.Imaging.ImageFormat]::Png)`, path)
	if err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", script).Run(); err != nil {
		return nil, ErrNoImage
	}
	return os.ReadFile(path)
}
