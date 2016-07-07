// Copyright 2016 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

package libkbfs

import (
	"sync"
	"time"

	"golang.org/x/net/context"
)

// BlockOpsConstrained implements the BlockOps interface by relaying
// requests to a delegate BlockOps, but it delays all Puts by
// simulating a bottleneck of the given bandwidth.
type BlockOpsConstrained struct {
	IFCERFTBlockOps
	bwKBps int
	lock   sync.Mutex
}

// NewBlockOpsConstrained constructs a new BlockOpsConstrained.
func NewBlockOpsConstrained(delegate IFCERFTBlockOps, bwKBps int) *BlockOpsConstrained {
	return &BlockOpsConstrained{
		IFCERFTBlockOps: delegate,
		bwKBps:          bwKBps,
	}
}

func (b *BlockOpsConstrained) delay(ctx context.Context, size int) error {
	if b.bwKBps <= 0 {
		return nil
	}
	b.lock.Lock()
	defer b.lock.Unlock()
	// Simulate a constrained bserver connection
	delay := size * int(time.Second) / (b.bwKBps * 1024)
	time.Sleep(time.Duration(delay))
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

// Put implements the BlockOps interface for BlockOpsConstrained.
func (b *BlockOpsConstrained) Put(ctx context.Context, md *RootMetadata,
	blockPtr BlockPointer, readyBlockData ReadyBlockData) error {
	if err := b.delay(ctx, len(readyBlockData.buf)); err != nil {
		return err
	}
	return b.IFCERFTBlockOps.Put(ctx, md, blockPtr, readyBlockData)
}
