package selfupdate

import "testing"

func TestExpectedAssetName(t *testing.T) {
	v, _ := ParseVersion("v1.0.24")
	tests := []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "llmtui-v1.0.24-darwin-arm64.tar.gz"},
		{"darwin", "amd64", "llmtui-v1.0.24-darwin-amd64.tar.gz"},
		{"linux", "amd64", "llmtui-v1.0.24-linux-amd64.tar.gz"},
		{"linux", "arm64", "llmtui-v1.0.24-linux-arm64.tar.gz"},
		{"windows", "amd64", "llmtui-v1.0.24-windows-amd64.zip"},
	}
	for _, tt := range tests {
		if got := ExpectedAssetName(v, tt.goos, tt.goarch); got != tt.want {
			t.Errorf("%s/%s: got %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestSelectAsset(t *testing.T) {
	v, _ := ParseVersion("v1.0.24")
	rel := Release{
		TagName: "v1.0.24",
		Assets: []Asset{
			{Name: "llmtui-v1.0.24-linux-amd64.tar.gz", DownloadURL: "https://github.com/x"},
			{Name: "llmtui-v1.0.24-darwin-arm64.tar.gz", DownloadURL: "https://github.com/y"},
			{Name: "checksums.txt", DownloadURL: "https://github.com/c"},
		},
	}

	a, c, err := SelectAsset(rel, v, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "llmtui-v1.0.24-linux-amd64.tar.gz" || c.Name != "checksums.txt" {
		t.Fatalf("got %q / %q", a.Name, c.Name)
	}

	if _, _, err := SelectAsset(rel, v, "linux", "riscv64"); err == nil {
		t.Fatal("expected unsupported-platform error")
	}
	if _, _, err := SelectAsset(rel, v, "windows", "amd64"); err == nil {
		t.Fatal("expected missing-asset error")
	}

	relNoSums := Release{TagName: "v1.0.24", Assets: []Asset{{Name: "llmtui-v1.0.24-linux-amd64.tar.gz"}}}
	if _, _, err := SelectAsset(relNoSums, v, "linux", "amd64"); err == nil {
		t.Fatal("expected missing-checksums error")
	}
}

func TestPlatformSupported(t *testing.T) {
	if !PlatformSupported("darwin", "arm64") {
		t.Error("darwin/arm64 should be supported")
	}
	if PlatformSupported("android", "arm64") {
		t.Error("android must not be a self-update target")
	}
	if PlatformSupported("plan9", "amd64") {
		t.Error("plan9 not supported")
	}
}
