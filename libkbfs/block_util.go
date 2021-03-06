// Copyright 2016 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

package libkbfs

import (
	"github.com/keybase/client/go/logger"
	"github.com/keybase/kbfs/kbfsblock"
	"github.com/keybase/kbfs/kbfscodec"
	"github.com/keybase/kbfs/kbfscrypto"
	"github.com/keybase/kbfs/tlf"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

func isRecoverableBlockError(err error) bool {
	_, isArchiveError := err.(kbfsblock.BServerErrorBlockArchived)
	_, isDeleteError := err.(kbfsblock.BServerErrorBlockDeleted)
	_, isRefError := err.(kbfsblock.BServerErrorBlockNonExistent)
	_, isMaxExceededError := err.(kbfsblock.BServerErrorMaxRefExceeded)
	return isArchiveError || isDeleteError || isRefError || isMaxExceededError
}

// putBlockToServer either puts the full block to the block server, or
// just adds a reference, depending on the refnonce in blockPtr.
func putBlockToServer(ctx context.Context, bserv BlockServer, tlfID tlf.ID,
	blockPtr BlockPointer, readyBlockData ReadyBlockData) error {
	var err error
	if blockPtr.RefNonce == kbfsblock.ZeroRefNonce {
		err = bserv.Put(ctx, tlfID, blockPtr.ID, blockPtr.Context,
			readyBlockData.buf, readyBlockData.serverHalf)
	} else {
		// non-zero block refnonce means this is a new reference to an
		// existing block.
		err = bserv.AddBlockReference(ctx, tlfID, blockPtr.ID,
			blockPtr.Context)
	}
	return err
}

// PutBlockCheckQuota is a thin wrapper around putBlockToServer (which
// calls either bserver.Put or bserver.AddBlockReference) that reports
// quota errors.
func PutBlockCheckQuota(ctx context.Context, bserv BlockServer,
	reporter Reporter, tlfID tlf.ID, blockPtr BlockPointer,
	readyBlockData ReadyBlockData, tlfName CanonicalTlfName) error {
	err := putBlockToServer(ctx, bserv, tlfID, blockPtr, readyBlockData)
	if qe, ok := err.(kbfsblock.BServerErrorOverQuota); ok && !qe.Throttled {
		reporter.ReportErr(ctx, tlfName, tlfID.IsPublic(),
			WriteMode, OverQuotaWarning{qe.Usage, qe.Limit})
		return nil
	}
	return err
}

func doOneBlockPut(ctx context.Context, bserv BlockServer, reporter Reporter,
	tlfID tlf.ID, tlfName CanonicalTlfName, blockState blockState,
	blocksToRemoveChan chan *FileBlock) error {
	err := PutBlockCheckQuota(ctx, bserv, reporter, tlfID, blockState.blockPtr,
		blockState.readyBlockData, tlfName)
	if err == nil && blockState.syncedCb != nil {
		err = blockState.syncedCb()
	}
	if err != nil && isRecoverableBlockError(err) {
		fblock, ok := blockState.block.(*FileBlock)
		if ok && !fblock.IsInd {
			blocksToRemoveChan <- fblock
		}
	}

	return err
}

// doBlockPuts writes all the pending block puts to the cache and
// server. If the err returned by this function satisfies
// isRecoverableBlockError(err), the caller should retry its entire
// operation, starting from when the MD successor was created.
//
// Returns a slice of block pointers that resulted in recoverable
// errors and should be removed by the caller from any saved state.
func doBlockPuts(ctx context.Context, bserv BlockServer, bcache BlockCache,
	reporter Reporter, log logger.Logger, tlfID tlf.ID, tlfName CanonicalTlfName,
	bps blockPutState) ([]BlockPointer, error) {
	eg, groupCtx := errgroup.WithContext(ctx)

	blocks := make(chan blockState, len(bps.blockStates))

	numWorkers := len(bps.blockStates)
	if numWorkers > maxParallelBlockPuts {
		numWorkers = maxParallelBlockPuts
	}
	// A channel to list any blocks that have been archived or
	// deleted.  Any of these will result in an error, so the maximum
	// we'll get is the same as the number of workers.
	blocksToRemoveChan := make(chan *FileBlock, numWorkers)

	worker := func() error {
		for blockState := range blocks {
			err := doOneBlockPut(groupCtx, bserv, reporter, tlfID,
				tlfName, blockState, blocksToRemoveChan)
			if err != nil {
				return err
			}
		}
		return nil
	}
	for i := 0; i < numWorkers; i++ {
		eg.Go(worker)
	}

	for _, blockState := range bps.blockStates {
		blocks <- blockState
	}
	close(blocks)

	err := eg.Wait()
	close(blocksToRemoveChan)
	var blocksToRemove []BlockPointer
	if isRecoverableBlockError(err) {
		// Wait for all the outstanding puts to finish, to amortize
		// the work of re-doing the put.
		for fblock := range blocksToRemoveChan {
			for i, bs := range bps.blockStates {
				if bs.block == fblock {
					// Let the caller know which blocks shouldn't be
					// retried.
					blocksToRemove = append(blocksToRemove,
						bps.blockStates[i].blockPtr)
				}
			}

			// Remove each problematic block from the cache so the
			// redo can just make a new block instead.
			if err := bcache.DeleteKnownPtr(tlfID, fblock); err != nil {
				log.CWarningf(ctx, "Couldn't delete ptr for a block: %v", err)
			}
			if err := bcache.DeleteTransient(
				blocksToRemove[len(blocksToRemove)-1], tlfID); err != nil {
				log.CWarningf(ctx, "Couldn't delete block: %v", err)
			}
		}
	}
	return blocksToRemove, err
}

func assembleBlock(ctx context.Context, keyGetter blockKeyGetter,
	codec kbfscodec.Codec, cryptoPure cryptoPure, kmd KeyMetadata,
	blockPtr BlockPointer, block Block, buf []byte,
	blockServerHalf kbfscrypto.BlockCryptKeyServerHalf) error {
	if err := kbfsblock.VerifyID(buf, blockPtr.ID); err != nil {
		return err
	}

	tlfCryptKey, err := keyGetter.GetTLFCryptKeyForBlockDecryption(
		ctx, kmd, blockPtr)
	if err != nil {
		return err
	}

	// construct the block crypt key
	blockCryptKey := kbfscrypto.UnmaskBlockCryptKey(
		blockServerHalf, tlfCryptKey)

	var encryptedBlock EncryptedBlock
	err = codec.Decode(buf, &encryptedBlock)
	if err != nil {
		return err
	}

	// decrypt the block
	err = cryptoPure.DecryptBlock(encryptedBlock, blockCryptKey, block)
	if err != nil {
		return err
	}

	block.SetEncodedSize(uint32(len(buf)))
	return nil
}
