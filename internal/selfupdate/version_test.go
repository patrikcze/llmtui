package selfupdate

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    Version
		wantErr bool
	}{
		{in: "v1.0.22", want: Version{1, 0, 22, ""}},
		{in: "1.0.22", want: Version{1, 0, 22, ""}},
		{in: "  v2.3.4  ", want: Version{2, 3, 4, ""}},
		{in: "v1.2.3-rc1", want: Version{1, 2, 3, "rc1"}},
		{in: "v1.2.3-rc.2", want: Version{1, 2, 3, "rc.2"}},
		{in: "v1.0.22-3-gabcdef", want: Version{1, 0, 22, "3-gabcdef"}},
		{in: "v1.2.3+build7", want: Version{1, 2, 3, ""}},
		{in: "dev", wantErr: true},
		{in: "", wantErr: true},
		{in: "v1.2", wantErr: true},
		{in: "v1.2.3.4", wantErr: true},
		{in: "vx.y.z", wantErr: true},
		{in: "abc123", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseVersion(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %+v", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.1", "v1.0.0", 1},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.1.0", "v1.0.9", 1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.0.24", "v1.0.22", 1},
		{"v1.0.0", "v1.0.0-rc1", 1}, // release beats prerelease
		{"v1.0.0-rc1", "v1.0.0", -1},
		{"v1.0.0-rc1", "v1.0.0-rc2", -1},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", -1}, // dotted numeric identifier ordering
		{"v1.0.0-alpha", "v1.0.0-beta", -1},
		{"v1.0.0-rc.1", "v1.0.0-rc.1.1", -1},
	}
	for _, tt := range tests {
		a, err := ParseVersion(tt.a)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.a, err)
		}
		b, err := ParseVersion(tt.b)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.b, err)
		}
		if got := a.Compare(b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := b.Compare(a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestIsDevBuild(t *testing.T) {
	for _, s := range []string{"", "dev", "none", "unknown", "abcdef0", "v1.2"} {
		if !IsDevBuild(s) {
			t.Errorf("IsDevBuild(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"v1.0.22", "1.0.0", "v2.0.0-rc1", "v1.0.22-3-gabcdef"} {
		if IsDevBuild(s) {
			t.Errorf("IsDevBuild(%q) = true, want false", s)
		}
	}
}

func TestVersionString(t *testing.T) {
	for _, s := range []string{"v1.0.22", "v1.2.3-rc1"} {
		v, err := ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		if v.String() != s {
			t.Errorf("String() = %q, want %q", v.String(), s)
		}
	}
}
