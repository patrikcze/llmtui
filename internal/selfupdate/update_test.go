package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRelease builds a release archive for the running platform plus a
// matching checksums.txt, and serves them (and the releases listing) from an
// httptest.Server. corruptChecksum flips the archive's recorded hash.
func fakeRelease(t *testing.T, tag string, corruptChecksum bool) (*httptest.Server, string) {
	t.Helper()
	goos := runtime.GOOS
	v, err := ParseVersion(tag)
	if err != nil {
		t.Fatal(err)
	}
	assetName := ExpectedAssetName(v, goos, runtime.GOARCH)
	top := strings.TrimSuffix(strings.TrimSuffix(assetName, ".tar.gz"), ".zip")

	var archive []byte
	if strings.HasSuffix(assetName, ".zip") {
		archive = buildZip(t, defaultReleaseEntries(top, goos))
	} else {
		archive = buildTarGz(t, defaultReleaseEntries(top, goos))
	}

	sum := sha256Hex(archive)
	if corruptChecksum {
		sum = strings.Repeat("0", 64)
	}
	checksums := []byte(fmt.Sprintf("%s  ./%s\n", sum, assetName))

	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case assetName:
			_, _ = w.Write(archive)
		case ChecksumsAsset:
			_, _ = w.Write(checksums)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		rel := []Release{{
			TagName: tag,
			Assets: []Asset{
				{Name: assetName, Size: int64(len(archive)), DownloadURL: base + "/dl/" + assetName},
				{Name: ChecksumsAsset, Size: int64(len(checksums)), DownloadURL: base + "/dl/" + ChecksumsAsset},
			},
		}}
		_ = json.NewEncoder(w).Encode(rel)
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv, assetName
}

func TestPlanAndExecuteUpdate(t *testing.T) {
	allowTestDownloadHost(t)
	srv, assetName := fakeRelease(t, "v9.9.9", false)

	prefix := t.TempDir()
	target, _ := TargetForScope(ScopeSystem, prefix)
	build := BuildInfo{Version: "v1.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH}
	client := testClient(srv.URL)

	plan, err := PlanUpdate(context.Background(), client, build, target, false)
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if !plan.UpdateAvailable || plan.AlreadyCurrent() {
		t.Fatalf("plan says no update: %+v", plan)
	}
	if plan.Archive.Name != assetName {
		t.Fatalf("asset = %q, want %q", plan.Archive.Name, assetName)
	}

	if err := ExecuteUpdate(context.Background(), client, plan, io.Discard); err != nil {
		t.Fatalf("ExecuteUpdate: %v", err)
	}

	if _, err := os.Stat(target.BinPath); err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target.RuntimeDir, "libllama.so")); err != nil {
		t.Fatalf("runtime not installed: %v", err)
	}
	m, err := ReadManifest(target.ManifestPath)
	if err != nil || m.Version != "v9.9.9" {
		t.Fatalf("manifest: %+v err=%v", m, err)
	}
}

func TestExecuteUpdateChecksumMismatchLeavesInstallUntouched(t *testing.T) {
	allowTestDownloadHost(t)
	srv, _ := fakeRelease(t, "v9.9.9", true)

	prefix := t.TempDir()
	target, _ := TargetForScope(ScopeSystem, prefix)
	build := BuildInfo{Version: "v1.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH}
	client := testClient(srv.URL)

	plan, err := PlanUpdate(context.Background(), client, build, target, false)
	if err != nil {
		t.Fatal(err)
	}
	err = ExecuteUpdate(context.Background(), client, plan, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v, want checksum mismatch", err)
	}
	if _, statErr := os.Stat(target.BinPath); !os.IsNotExist(statErr) {
		t.Fatalf("binary was installed despite checksum failure (err=%v)", statErr)
	}
}

func TestPlanUpdateAlreadyCurrent(t *testing.T) {
	srv, _ := fakeRelease(t, "v1.0.0", false)
	prefix := t.TempDir()
	target, _ := TargetForScope(ScopeSystem, prefix)
	build := BuildInfo{Version: "v1.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH}

	plan, err := PlanUpdate(context.Background(), testClient(srv.URL), build, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.AlreadyCurrent() {
		t.Fatalf("expected AlreadyCurrent, got %+v", plan)
	}
}

func TestCheckReportsUpdate(t *testing.T) {
	srv, _ := fakeRelease(t, "v2.0.0", false)
	build := BuildInfo{Version: "v1.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH}
	res, err := Check(context.Background(), testClient(srv.URL), build, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.UpdateAvailable {
		t.Errorf("UpdateAvailable = false")
	}
	if res.ExpectedAsset == "" {
		t.Errorf("ExpectedAsset empty")
	}
}

func TestCheckDevBuild(t *testing.T) {
	srv, _ := fakeRelease(t, "v2.0.0", false)
	build := BuildInfo{Version: "dev", OS: runtime.GOOS, Arch: runtime.GOARCH}
	res, err := Check(context.Background(), testClient(srv.URL), build, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsDevBuild {
		t.Errorf("IsDevBuild = false for dev")
	}
	if res.UpdateAvailable {
		t.Errorf("dev build must not report a definite update")
	}
}
