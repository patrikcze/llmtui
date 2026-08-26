//go:build !windows

package tools

import "golang.org/x/sys/unix"

func localDisk(path string) (*diskContext, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return nil, err
	}
	return &diskContext{TotalBytes: uint64(stat.Blocks) * uint64(stat.Bsize), AvailableBytes: uint64(stat.Bavail) * uint64(stat.Bsize)}, nil
}
