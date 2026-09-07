//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris || aix

package personalapps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

type mutationLedgerLock struct {
	file *os.File
}

func lockMutationLedger(ctx context.Context, path string) (*mutationLedgerLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open mutation ledger lock: %w", err)
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &mutationLedgerLock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			file.Close()
			return nil, fmt.Errorf("lock mutation ledger: %w", err)
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (l *mutationLedgerLock) Unlock() error {
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
