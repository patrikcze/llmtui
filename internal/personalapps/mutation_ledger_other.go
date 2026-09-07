//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris || aix)

package personalapps

import (
	"context"
	"fmt"
)

type mutationLedgerLock struct{}

func lockMutationLedger(context.Context, string) (*mutationLedgerLock, error) {
	return nil, fmt.Errorf("mutation ledger locking is unsupported on this platform")
}

func (*mutationLedgerLock) Unlock() error { return nil }
