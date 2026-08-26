//go:build windows

package tools

import "errors"

func localDisk(string) (*diskContext, error) {
	return nil, errors.New("filesystem information unavailable")
}
