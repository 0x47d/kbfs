// Copyright 2016 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

package libcbfs

import (
	"github.com/keybase/kbfs/libcbfs/cbfs"
	"golang.org/x/net/context"
)

// Alias is a top-level folder accessed through its non-canonical name.
type Alias struct {
	// canonical name for this folder
	canon string
	emptyFile
}

// GetFileInformation for cbfs.
func (s *Alias) GetFileInformation(context.Context) (a *cbfs.Stat, err error) {
	return defaultSymlinkDirInformation()
}
