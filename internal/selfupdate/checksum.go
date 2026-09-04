package selfupdate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// maxChecksumsBytes bounds a checksums.txt download. The real file is a
// handful of lines.
const maxChecksumsBytes = 64 << 10

// Checksums maps a release asset basename to its lowercase hex SHA-256.
type Checksums map[string]string

// ParseChecksums parses the output format of `shasum -a 256` (and coreutils
// sha256sum): "<64 hex>  <name>" or "<64 hex> *<name>". A leading "./" on the
// name (as produced by the release workflow's `find .`) is stripped, and only
// the basename is retained as the key.
func ParseChecksums(r io.Reader) (Checksums, error) {
	out := Checksums{}
	sc := bufio.NewScanner(io.LimitReader(r, maxChecksumsBytes+1))
	sc.Buffer(make([]byte, 0, 4096), 128<<10)
	total := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		total += len(line)
		if total > maxChecksumsBytes {
			return nil, fmt.Errorf("checksums file exceeds %d bytes", maxChecksumsBytes)
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("malformed checksums line: %q", line)
		}
		sum := strings.ToLower(fields[0])
		if !isHexSHA256(sum) {
			return nil, fmt.Errorf("malformed SHA-256 in checksums line: %q", line)
		}
		name := strings.Join(fields[1:], " ")
		name = strings.TrimPrefix(name, "*")
		name = strings.TrimPrefix(name, "./")
		name = path.Base(name)
		if name == "" || name == "." || name == "/" {
			return nil, fmt.Errorf("malformed name in checksums line: %q", line)
		}
		out[name] = sum
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("checksums file has no entries")
	}
	return out, nil
}

// For returns the expected checksum for an asset basename.
func (c Checksums) For(name string) (string, bool) {
	s, ok := c[path.Base(name)]
	return s, ok
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// sha256File returns the lowercase hex SHA-256 of a file's contents.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFileChecksum confirms that path hashes to want (lowercase hex
// SHA-256). A mismatch is a hard error.
func VerifyFileChecksum(path, want string) error {
	got, err := sha256File(path)
	if err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", path, want, got)
	}
	return nil
}
