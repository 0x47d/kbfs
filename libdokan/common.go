// Copyright 2015 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

// +build windows

package libdokan

import (
	"syscall"
	"time"

	"github.com/keybase/client/go/logger"
	"github.com/keybase/kbfs/dokan"
	"github.com/keybase/kbfs/libkbfs"
	"golang.org/x/net/context"
)

const (
	// PublicName is the name of the parent of all public top-level folders.
	PublicName = "public"

	// PrivateName is the name of the parent of all private top-level folders.
	PrivateName = "private"

	// CtxAppIDKey is the context app id
	CtxAppIDKey = "kbfsfuse-app-id"

	// CtxOpID is the display name for the unique operation FUSE ID tag.
	CtxOpID = "FID"
)

// CtxTagKey is the type used for unique context tags
type CtxTagKey int

const (
	// CtxIDKey is the type of the tag for unique operation IDs.
	CtxIDKey CtxTagKey = iota
)

// NewContextWithOpID adds a unique ID to this context, identifying
// a particular request.
func NewContextWithOpID(ctx context.Context,
	log logger.Logger) context.Context {
	id, err := libkbfs.MakeRandomRequestID()
	if err != nil {
		log.Errorf("Couldn't make request ID: %v", err)
		return ctx
	}
	return context.WithValue(ctx, CtxIDKey, id)
}

// deToStat converts from a libkbfs.Direntry and error to a *dokan.Stat and error.
// Note that handling symlinks to directories requires extra processing not done here.
func deToStat(ei libkbfs.EntryInfo, err error) (*dokan.Stat, error) {
	if err != nil {
		return nil, errToDokan(err)
	}
	st := &dokan.Stat{}
	fillStat(st, &ei)
	return st, nil
}

// fillStat fill a dokan.Stat from a libkbfs.DirEntry.
// Note that handling symlinks to directories requires extra processing not done here.
func fillStat(a *dokan.Stat, de *libkbfs.EntryInfo) {
	a.FileSize = int64(de.Size)
	a.LastWrite = time.Unix(0, de.Mtime)
	a.LastAccess = a.LastWrite
	a.Creation = time.Unix(0, de.Ctime)
	a.NumberOfLinks = 1
	switch de.Type {
	case libkbfs.File, libkbfs.Exec:
		a.FileAttributes = fileAttributeNormal
	case libkbfs.Dir:
		a.FileAttributes = fileAttributeDirectory
	case libkbfs.Sym:
		a.FileAttributes = fileAttributeReparsePoint
		a.ReparsePointTag = reparsePointTagSymlink
	}
}

// errToDokan makes some libkbfs errors easier to digest in dokan. Not needed in most places.
func errToDokan(err error) error {
	switch err.(type) {
	case libkbfs.NoSuchNameError:
		return dokan.ErrObjectPathNotFound
	case libkbfs.NoSuchUserError:
		return dokan.ErrObjectPathNotFound
	case libkbfs.MDServerErrorUnauthorized:
		return dokan.ErrAccessDenied
	case nil:
		return nil
	}
	return err
}

// defaultDirectoryInformation returns default directory information.
func defaultDirectoryInformation() (*dokan.Stat, error) {
	var st dokan.Stat
	st.FileAttributes = fileAttributeDirectory
	st.NumberOfLinks = 1
	return &st, nil
}

// defaultFileInformation returns default file information.
func defaultFileInformation() (*dokan.Stat, error) {
	var st dokan.Stat
	st.FileAttributes = fileAttributeNormal
	st.NumberOfLinks = 1
	return &st, nil
}

// defaultSymlinkFileInformation returns default symlink to file information.
func defaultSymlinkFileInformation() (*dokan.Stat, error) {
	var st dokan.Stat
	st.FileAttributes = fileAttributeReparsePoint
	st.ReparsePointTag = reparsePointTagSymlink
	st.NumberOfLinks = 1
	return &st, nil
}

// defaultSymlinkDirInformation returns default symlink to directory information.
func defaultSymlinkDirInformation() (*dokan.Stat, error) {
	var st dokan.Stat
	st.FileAttributes = fileAttributeReparsePoint | fileAttributeDirectory
	st.ReparsePointTag = reparsePointTagSymlink
	st.NumberOfLinks = 1
	return &st, nil
}

const (
	fileAttributeNormal       = syscall.FILE_ATTRIBUTE_NORMAL
	fileAttributeDirectory    = syscall.FILE_ATTRIBUTE_DIRECTORY
	fileAttributeReparsePoint = syscall.FILE_ATTRIBUTE_REPARSE_POINT
	fileAttributeReadonly     = syscall.FILE_ATTRIBUTE_READONLY
	reparsePointTagSymlink    = syscall.IO_REPARSE_TAG_SYMLINK
)
