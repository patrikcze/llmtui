package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"runtime"
	"testing"
)

// fakeBinary returns bytes that pass ValidateBinary for goos: a valid magic
// number and enough size.
func fakeBinary(goos string) []byte {
	var magic []byte
	switch goos {
	case "windows":
		magic = []byte("MZ\x90\x00")
	case "darwin":
		magic = []byte{0xCF, 0xFA, 0xED, 0xFE}
	default: // linux, android
		magic = []byte("\x7fELF")
	}
	b := make([]byte, 700<<10)
	copy(b, magic)
	return b
}

type archiveEntry struct {
	name    string // path inside the archive, including the top dir
	body    []byte
	dir     bool
	symlink string // non-empty: emit as a symlink to this target
	mode    int64
}

func defaultReleaseEntries(topDir, goos string) []archiveEntry {
	bin := "llmtui"
	if goos == "windows" {
		bin = "llmtui.exe"
	}
	return []archiveEntry{
		{name: topDir + "/", dir: true},
		{name: topDir + "/" + bin, body: fakeBinary(goos), mode: 0o755},
		{name: topDir + "/LICENSE", body: []byte("MIT-ish\n"), mode: 0o644},
		{name: topDir + "/THIRD_PARTY_NOTICES.md", body: []byte("notices\n"), mode: 0o644},
		{name: topDir + "/lib/", dir: true},
		{name: topDir + "/lib/llmtui/", dir: true},
		{name: topDir + "/lib/llmtui/runtime/", dir: true},
		{name: topDir + "/lib/llmtui/runtime/libllama.so", body: bytes.Repeat([]byte("x"), 2048), mode: 0o755},
		{name: topDir + "/lib/llmtui/runtime/LLAMA_VERSION", body: []byte("b10066\n"), mode: 0o644},
		{name: topDir + "/examples/README.md", body: []byte("examples\n"), mode: 0o644}, // must be ignored
	}
}

func buildTarGz(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.dir:
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
		case e.symlink != "":
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.symlink
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildZip(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		if e.dir {
			if _, err := zw.Create(e.name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.symlink != "" {
			hdr.SetMode(os.ModeSymlink | 0o777)
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(e.symlink))
			continue
		}
		if e.mode != 0 {
			hdr.SetMode(os.FileMode(e.mode))
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTemp(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := path.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// hostGOOS is the running OS, used where a test needs the archive to match
// the platform ValidateBinary checks against.
var hostGOOS = runtime.GOOS
