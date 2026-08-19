//go:build windows

package runtime

import "os"

func isCurrentUserOwner(os.FileInfo) (bool, error) {
	return true, nil
}
