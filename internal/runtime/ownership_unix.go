//go:build !windows

package runtime

import (
	"fmt"
	"os"
	"syscall"
)

func isCurrentUserOwner(info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("read runtime directory owner")
	}
	return stat.Uid == uint32(os.Getuid()), nil
}
