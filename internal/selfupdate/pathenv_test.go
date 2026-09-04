package selfupdate

import "testing"

func TestMergePathEntry(t *testing.T) {
	tests := []struct {
		name         string
		current, dir string
		ci           bool
		wantResult   string
		wantChanged  bool
	}{
		{
			name:    "append when absent",
			current: "/usr/bin:/bin", dir: "/opt/llmtui/bin",
			wantResult: "/usr/bin:/bin:/opt/llmtui/bin", wantChanged: true,
		},
		{
			name:    "no-op when already present",
			current: "/usr/bin:/opt/llmtui/bin:/bin", dir: "/opt/llmtui/bin",
			wantResult: "/usr/bin:/opt/llmtui/bin:/bin", wantChanged: false,
		},
		{
			name:    "trailing slash treated as same",
			current: "/usr/bin:/opt/llmtui/bin/", dir: "/opt/llmtui/bin",
			wantChanged: false, wantResult: "/usr/bin:/opt/llmtui/bin/",
		},
		{
			name:    "case-insensitive dedupe",
			current: `C:\Windows;C:\Program Files\llmtui`, dir: `c:\program files\LLMTUI`,
			ci: true, wantChanged: false, wantResult: `C:\Windows;C:\Program Files\llmtui`,
		},
		{
			name:    "case-sensitive keeps distinct",
			current: "/usr/bin:/opt/LLMTUI", dir: "/opt/llmtui",
			wantChanged: true, wantResult: "/usr/bin:/opt/LLMTUI:/opt/llmtui",
		},
		{
			name:    "empty current",
			current: "", dir: "/opt/llmtui/bin",
			wantChanged: true, wantResult: "/opt/llmtui/bin",
		},
		{
			name:    "preserves all existing entries and order",
			current: "/a:/b:/c:/d", dir: "/e",
			wantChanged: true, wantResult: "/a:/b:/c:/d:/e",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sep := ":"
			if tt.ci {
				sep = ";"
			}
			got, changed := mergePathEntry(tt.current, tt.dir, sep, tt.ci)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if got != tt.wantResult {
				t.Errorf("result = %q, want %q", got, tt.wantResult)
			}
		})
	}
}
