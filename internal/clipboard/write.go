package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// WriteText puts text on the system clipboard.
func WriteText(ctx context.Context, text string) error {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	switch runtime.GOOS {
	case "darwin":
		return pipeTo(ctx, text, "pbcopy")
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return pipeTo(ctx, text, "wl-copy")
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			return pipeTo(ctx, text, "xclip", "-selection", "clipboard")
		}
		return errors.New("copying needs wl-copy or xclip installed")
	case "windows":
		return pipeTo(ctx, text, "clip")
	default:
		return fmt.Errorf("clipboard is not supported on %s", runtime.GOOS)
	}
}

func pipeTo(ctx context.Context, text string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
