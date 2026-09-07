package personalapps

import (
	"errors"
	"os"
	"testing"
)

func TestCheckCalendarHelper(t *testing.T) {
	tests := []struct {
		name      string
		platform  string
		path      string
		mode      os.FileMode
		err       error
		wantCode  string
		wantReady bool
	}{
		{name: "unsupported platform", platform: "linux", path: "/helper", wantCode: "unsupported_platform"},
		{name: "missing path", platform: "darwin", wantCode: "missing_path"},
		{name: "relative path", platform: "darwin", path: "helper", wantCode: "relative_path"},
		{name: "missing helper", platform: "darwin", path: "/helper", err: os.ErrNotExist, wantCode: "missing_helper"},
		{name: "inaccessible helper", platform: "darwin", path: "/helper", err: errors.New("permission denied"), wantCode: "unavailable_helper"},
		{name: "directory", platform: "darwin", path: "/helper", mode: os.ModeDir | 0o755, wantCode: "invalid_helper"},
		{name: "not executable", platform: "darwin", path: "/helper", mode: 0o600, wantCode: "not_executable"},
		{name: "ready", platform: "darwin", path: "/helper", mode: 0o755, wantCode: "ready", wantReady: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := checkCalendarHelper(test.platform, test.path, func(string) (os.FileMode, error) {
				return test.mode, test.err
			})
			if got.Code != test.wantCode || got.Ready != test.wantReady {
				t.Fatalf("checkCalendarHelper() = %+v, want code=%q ready=%t", got, test.wantCode, test.wantReady)
			}
		})
	}
}
