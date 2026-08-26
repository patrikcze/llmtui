package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"unicode/utf8"
)

// ErrNoText is returned when the clipboard cannot provide UTF-8 text.
var ErrNoText = errors.New("no text on the clipboard")

// ReadText returns bounded UTF-8 clipboard text without loading an image or
// arbitrary binary payload into memory.
func ReadText(ctx context.Context, maxBytes int) (text string, truncated bool, err error) {
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "pbpaste")
	case "linux":
		if _, lookErr := exec.LookPath("wl-paste"); lookErr == nil {
			cmd = exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", "text")
		} else if _, lookErr := exec.LookPath("xclip"); lookErr == nil {
			cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-o")
		} else {
			return "", false, errors.New("reading clipboard text needs wl-paste or xclip installed")
		}
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw -Format Text")
	default:
		return "", false, fmt.Errorf("clipboard text is not supported on %s", runtime.GOOS)
	}
	var output cappedBuffer
	output.limit = maxBytes + 1
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("read clipboard text: %w", err)
	}
	data := output.Bytes()
	if len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", false, ErrNoText
	}
	if len(data) > maxBytes {
		data = data[:maxBytes]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		truncated = true
	}
	return string(data), truncated, nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.Buffer.Write(data)
	}
	return original, nil
}
