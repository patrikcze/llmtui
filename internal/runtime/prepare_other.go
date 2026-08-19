//go:build !windows

package runtime

// PrepareLibraryDir configures native dependency lookup for dir when needed.
func PrepareLibraryDir(string) error {
	return nil
}
