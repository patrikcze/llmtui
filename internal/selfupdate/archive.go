package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Extraction limits. Vars, not consts, so tests can shrink them to exercise
// the bomb/oversize guards without materialising gigabytes.
var (
	maxExtractFileBytes  int64 = 2 << 30 // one extracted file
	maxExtractTotalBytes int64 = 4 << 30 // whole payload
	maxExtractEntries          = 20000
)

// Payload is a release archive extracted into a private staging directory,
// reduced to the files self-update installs.
type Payload struct {
	Dir        string   // staging root (caller owns cleanup)
	BinPath    string   // absolute path to the extracted llmtui[.exe]
	RuntimeDir string   // absolute path to lib/llmtui/runtime, "" if the archive had none
	DocFiles   []string // absolute paths to LICENSE / THIRD_PARTY_NOTICES.md
}

// binaryName is the executable name inside a release archive for a platform.
func binaryName(goos string) string {
	if goos == "windows" {
		return "llmtui.exe"
	}
	return "llmtui"
}

// payloadAllowed reports whether a slash-separated archive-relative path (with
// the single top-level release directory already stripped) is one self-update
// installs, and classifies it.
func payloadAllowed(rel, goos string) (kind string, ok bool) {
	switch rel {
	case binaryName(goos):
		return "binary", true
	case "LICENSE", "THIRD_PARTY_NOTICES.md":
		return "doc", true
	}
	if rel == "lib/llmtui" || strings.HasPrefix(rel, "lib/llmtui/") {
		return "runtime", true
	}
	return "", false
}

// ExtractRelease extracts the wanted files from a downloaded release archive
// (tar.gz or zip) into a fresh directory under parentDir. It rejects path
// traversal, absolute paths, symlinks, hardlinks, device nodes, oversized
// entries and archive bombs, and confirms the llmtui executable is present.
func ExtractRelease(archivePath, parentDir, goos string) (*Payload, error) {
	stage, err := os.MkdirTemp(parentDir, ".llmtui-stage-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		_ = os.RemoveAll(stage)
		return nil, fmt.Errorf("chmod staging dir: %w", err)
	}

	root, err := os.OpenRoot(stage)
	if err != nil {
		_ = os.RemoveAll(stage)
		return nil, fmt.Errorf("open staging root: %w", err)
	}

	ex := &extractor{root: root, goos: goos}
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz"):
		err = ex.tarGz(archivePath)
	case strings.HasSuffix(archivePath, ".zip"):
		err = ex.zip(archivePath)
	default:
		err = fmt.Errorf("unsupported archive format: %s", filepath.Base(archivePath))
	}
	_ = root.Close()
	if err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}

	p := &Payload{Dir: stage}
	binRel := binaryName(goos)
	if _, err := os.Stat(filepath.Join(stage, binRel)); err != nil {
		_ = os.RemoveAll(stage)
		return nil, fmt.Errorf("release archive did not contain %s", binRel)
	}
	p.BinPath = filepath.Join(stage, binRel)
	if err := os.Chmod(p.BinPath, 0o755); err != nil {
		_ = os.RemoveAll(stage)
		return nil, fmt.Errorf("chmod extracted binary: %w", err)
	}

	runtimeDir := filepath.Join(stage, "lib", "llmtui", "runtime")
	if info, err := os.Stat(runtimeDir); err == nil && info.IsDir() {
		p.RuntimeDir = runtimeDir
	}
	for _, d := range []string{"LICENSE", "THIRD_PARTY_NOTICES.md"} {
		if _, err := os.Stat(filepath.Join(stage, d)); err == nil {
			p.DocFiles = append(p.DocFiles, filepath.Join(stage, d))
		}
	}
	return p, nil
}

type extractor struct {
	root     *os.Root
	goos     string
	entries  int
	total    int64
	topDir   string
	sawFiles bool
}

// strip removes the single shared top-level release directory
// ("llmtui-v1.2.3-linux-amd64/") from an archive entry name and returns the
// cleaned slash-separated remainder. It rejects traversal and absolute paths.
func (e *extractor) strip(name string) (string, error) {
	norm := strings.ReplaceAll(name, "\\", "/")
	norm = strings.TrimPrefix(norm, "./")
	cleaned := path.Clean(norm)
	if norm == "" || strings.HasPrefix(norm, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned == "." {
		return "", fmt.Errorf("archive contains unsafe path %q", name)
	}
	first, rest, _ := strings.Cut(cleaned, "/")
	if e.topDir == "" {
		e.topDir = first
	}
	if first != e.topDir {
		return "", fmt.Errorf("archive entry %q is outside the release directory %q", name, e.topDir)
	}
	return rest, nil // "" for the top dir entry itself
}

func (e *extractor) writeFile(rel string, r io.Reader, size int64, mode fs.FileMode) error {
	e.entries++
	if e.entries > maxExtractEntries {
		return fmt.Errorf("archive has more than %d entries", maxExtractEntries)
	}
	if size < 0 || size > maxExtractFileBytes {
		return fmt.Errorf("archive entry %q has invalid size %d", rel, size)
	}
	if e.total > maxExtractTotalBytes-size {
		return fmt.Errorf("archive payload exceeds %d bytes", maxExtractTotalBytes)
	}
	if dir := path.Dir(rel); dir != "." {
		if err := e.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	perm := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	f, err := e.root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", rel, err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(r, size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("extract %s: %w", rel, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", rel, closeErr)
	}
	if n != size {
		return fmt.Errorf("archive entry %q declared %d bytes, wrote %d", rel, size, n)
	}
	e.total += n
	e.sawFiles = true
	return nil
}

// runtimeSymlinkTarget validates a symlink archive entry. The only symlinks
// self-update accepts are the llama.cpp SONAME aliases the release archive
// places directly in lib/llmtui/runtime, each pointing at a sibling file by
// bare name (e.g. libggml-rpc.0.dylib -> libggml-rpc.dylib).
func runtimeSymlinkTarget(rel, linkname string) (string, bool) {
	if path.Dir(rel) != "lib/llmtui/runtime" {
		return "", false
	}
	t := strings.TrimSpace(strings.ReplaceAll(linkname, "\\", "/"))
	if t == "" || strings.Contains(t, "/") || t == "." || t == ".." {
		return "", false
	}
	return t, true
}

func (e *extractor) writeSymlink(rel, target string) error {
	e.entries++
	if e.entries > maxExtractEntries {
		return fmt.Errorf("archive has more than %d entries", maxExtractEntries)
	}
	if dir := path.Dir(rel); dir != "." {
		if err := e.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := e.root.Symlink(target, rel); err != nil {
		return fmt.Errorf("create symlink %s: %w", rel, err)
	}
	return nil
}

func (e *extractor) tarGz(archivePath string) (err error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		rel, err := e.strip(hdr.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		kind, ok := payloadAllowed(rel, e.goos)
		if !ok {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			if err := e.writeFile(rel, tr, hdr.Size, fs.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			target, ok := runtimeSymlinkTarget(rel, hdr.Linkname)
			if !ok {
				return fmt.Errorf("archive entry %q is an unsafe symlink to %q", hdr.Name, hdr.Linkname)
			}
			if err := e.writeSymlink(rel, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q (%s) is not a regular file", hdr.Name, kind)
		}
	}
	if !e.sawFiles {
		return fmt.Errorf("archive contained none of the expected files")
	}
	return nil
}

func (e *extractor) zip(archivePath string) (err error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer func() { err = errors.Join(err, zr.Close()) }()

	for _, entry := range zr.File {
		rel, err := e.strip(entry.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		kind, ok := payloadAllowed(rel, e.goos)
		if !ok {
			continue
		}
		mode := entry.Mode()
		if strings.HasSuffix(entry.Name, "/") || mode.IsDir() {
			continue
		}
		if mode&fs.ModeSymlink != 0 {
			rc, err := entry.Open()
			if err != nil {
				return fmt.Errorf("open %s in zip: %w", entry.Name, err)
			}
			linkBytes, readErr := io.ReadAll(io.LimitReader(rc, 4096))
			_ = rc.Close()
			if readErr != nil {
				return fmt.Errorf("read symlink %s: %w", entry.Name, readErr)
			}
			target, ok := runtimeSymlinkTarget(rel, string(linkBytes))
			if !ok {
				return fmt.Errorf("archive entry %q is an unsafe symlink to %q", entry.Name, linkBytes)
			}
			if err := e.writeSymlink(rel, target); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("archive entry %q (%s) is not a regular file", entry.Name, kind)
		}
		if entry.UncompressedSize64 > uint64(maxExtractFileBytes) {
			return fmt.Errorf("archive entry %q is too large", entry.Name)
		}
		rc, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open %s in zip: %w", entry.Name, err)
		}
		writeErr := e.writeFile(rel, rc, int64(entry.UncompressedSize64), mode)
		_ = rc.Close()
		if writeErr != nil {
			return writeErr
		}
	}
	if !e.sawFiles {
		return fmt.Errorf("archive contained none of the expected files")
	}
	return nil
}

// ValidateBinary sanity-checks an extracted executable before it is allowed
// anywhere near the install location: non-trivial size and a leading magic
// number consistent with the target OS. It deliberately does not execute the
// file.
func ValidateBinary(path, goos string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() < 512<<10 {
		return fmt.Errorf("staged binary is implausibly small (%d bytes)", info.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return fmt.Errorf("read binary header: %w", err)
	}
	if !magicMatchesOS(magic, goos) {
		return fmt.Errorf("staged binary is not a valid %s executable", goos)
	}
	return nil
}

func magicMatchesOS(m []byte, goos string) bool {
	switch goos {
	case "linux", "android":
		return string(m) == "\x7fELF"
	case "windows":
		return m[0] == 'M' && m[1] == 'Z'
	case "darwin":
		switch {
		case m[0] == 0xCF && m[1] == 0xFA && m[2] == 0xED && m[3] == 0xFE: // Mach-O 64 LE
			return true
		case m[0] == 0xCE && m[1] == 0xFA && m[2] == 0xED && m[3] == 0xFE: // Mach-O 32 LE
			return true
		case m[0] == 0xCA && m[1] == 0xFE && m[2] == 0xBA && m[3] == 0xBE: // fat binary
			return true
		default:
			return false
		}
	default:
		return true // unknown platform: don't block
	}
}
