// Copyright 2015 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

// +build windows

package libdokan

import (
	"github.com/keybase/kbfs/dokan"
	"golang.org/x/net/context"
)

// EmptyFolder represents an empty, read-only KBFS TLF that has not
// been created by someone with sufficient permissions.
type EmptyFolder struct {
	emptyFile
}

func (ef *EmptyFolder) open(ctx context.Context, oc *openContext, path []string) (f dokan.File, isDir bool, err error) {
	if len(path) != 0 {
		return nil, false, dokan.ErrObjectNameNotFound
	}
	return oc.returnDirNoCleanup(ef)
}

// GetFileInformation for dokan.
func (*EmptyFolder) GetFileInformation(*dokan.FileInfo) (a *dokan.Stat, err error) {
	return defaultDirectoryInformation()
}

// FindFiles for dokan.
func (*EmptyFolder) FindFiles(fi *dokan.FileInfo, callback func(*dokan.NamedStat) error) (err error) {
	return nil
}
