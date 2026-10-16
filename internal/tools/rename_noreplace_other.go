//go:build !linux && !darwin && !windows

package tools

import "os"

// renameNoReplace has no atomic no-replace primitive wired on this platform,
// so it uses the checked fallback.
func renameNoReplace(root *os.Root, _ string, src, dst string) error {
	return renameCheckedFallback(root, src, dst)
}
