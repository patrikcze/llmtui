package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lib", "llmtui", "install.json")
	in := InstallManifest{
		Version:     "v1.0.24",
		Scope:       string(ScopeSystem),
		Prefix:      "/usr/local",
		Asset:       "llmtui-v1.0.24-linux-amd64.tar.gz",
		OS:          "linux",
		Arch:        "amd64",
		InstalledBy: "self update",
	}
	if err := WriteManifest(p, in); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if fi, err := os.Stat(p); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o644 {
		t.Errorf("perm = %v", fi.Mode().Perm())
	}

	out, err := ReadManifest(p)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if out.Version != in.Version || out.Scope != in.Scope || out.Asset != in.Asset {
		t.Errorf("round trip mismatch: %+v", out)
	}
	if out.SchemaVersion != manifestSchemaVersion {
		t.Errorf("schema version = %d", out.SchemaVersion)
	}
	if out.InstalledAt == "" {
		t.Error("InstalledAt not stamped")
	}
}

func TestReadManifestMissing(t *testing.T) {
	if _, err := ReadManifest(filepath.Join(t.TempDir(), "nope.json")); !os.IsNotExist(err) {
		t.Fatalf("got %v, want not-exist", err)
	}
}
