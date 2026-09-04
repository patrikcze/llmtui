package selfupdate

import "path/filepath"

// DirOnPath reports whether dir is an element of the current process's PATH.
func DirOnPath(dir string) bool {
	return binDirOnPath(dir)
}

// UpdatePath ensures the directory holding the installed binary is on the
// persistent PATH. On Unix this is always a no-op (llmtui never edits shell
// profiles); on Windows it edits the machine or user Environment/Path
// registry value in place, preserving every existing entry.
func UpdatePath(target Target) (changed bool, note string, err error) {
	return updateSystemPath(filepath.Dir(target.BinPath), target.Scope)
}
