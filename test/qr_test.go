// Copyright 2016 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

// These tests exercise quota reclamation.

package test

import (
	"testing"
	"time"
)

// Check that simple quota reclamation works
func TestQRSimple(t *testing.T) {
	test(t,
		writers("alice"),
		as(alice,
			addTime(1*time.Minute),
			mkfile("a", "hello"),
			rm("a"),
			addTime(2*time.Minute),
			forceQuotaReclamation(),
		),
	)
}

// Check that quota reclamation works, eventually, after enough iterations.
func TestQRLargePointerSet(t *testing.T) {
	var busyWork []fileOp
	iters := 100
	for i := 0; i < iters; i++ {
		busyWork = append(busyWork, mkfile("a", "hello"), rm("a"))
	}
	// 5 unreferenced pointers per iteration -- 3 updates to the root
	// block, one empty file written to, and one non-empty file
	// deleted.
	ptrsPerIter := 5
	var qrOps []optionOp
	// Each reclamation needs a sync after it (e.g., a new "as"
	// clause) to ensure it completes before the next force
	// reclamation.
	for i := 0; i < ptrsPerIter*iters/100; i++ {
		qrOps = append(qrOps, as(alice,
			addTime(2*time.Minute),
			forceQuotaReclamation(),
		))
	}
	totalOps := []optionOp{writers("alice"), as(alice, busyWork...)}
	totalOps = append(totalOps, qrOps...)
	test(t, totalOps...)
}

// Test that quota reclamation handles conflict resolution correctly.
func TestQRAfterCR(t *testing.T) {
	test(t,
		writers("alice", "bob"),
		as(alice,
			mkfile("a/b", "hello"),
		),
		as(bob,
			disableUpdates(),
		),
		as(alice,
			write("a/c", "world"),
		),
		as(bob, noSync(),
			rm("a/b"),
			reenableUpdates(),
		),
		as(bob,
			addTime(2*time.Minute),
			forceQuotaReclamation(),
		),
	)
}

// Check that quota reclamation works on multi-block files
func TestQRWithMultiBlockFiles(t *testing.T) {
	test(t,
		blockSize(20), writers("alice"),
		as(alice,
			addTime(1*time.Minute),
			mkfile("a", ntimesString(15, "0123456789")),
			rm("a"),
			addTime(2*time.Minute),
			forceQuotaReclamation(),
		),
	)
}
