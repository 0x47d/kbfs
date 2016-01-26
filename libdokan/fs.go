// Copyright 2015 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

// +build windows

package libdokan

import (
	"errors"
	"strings"
	"sync"
	"syscall"

	"github.com/eapache/channels"
	"github.com/keybase/client/go/logger"
	"github.com/keybase/kbfs/dokan"
	"github.com/keybase/kbfs/libkbfs"
	"golang.org/x/net/context"
)

// FS implements the newfuse FS interface for KBFS.
type FS struct {
	config libkbfs.Config
	log    logger.Logger

	// notifications is a channel for notification functions (which
	// take no value and have no return value).
	notifications channels.Channel

	// notificationGroup can be used by tests to know when libfuse is
	// done processing asynchronous notifications.
	notificationGroup sync.WaitGroup

	// protects access to the notifications channel member (though not
	// sending/receiving)
	notificationMutex sync.RWMutex

	root *Root

	// context is the top level context for this filesystem
	context context.Context

	// currentUserSID stores the Windows identity of the user running
	// this process.
	currentUserSID *syscall.SID
}

// NewFS creates an FS
func NewFS(ctx context.Context, config libkbfs.Config, log logger.Logger) (*FS, error) {
	sid, err := dokan.CurrentProcessUserSid()
	if err != nil {
		return nil, err
	}
	f := &FS{
		config:         config,
		log:            log,
		currentUserSID: sid,
	}

	f.root = &Root{
		private: &FolderList{
			fs:      f,
			folders: make(map[string]fileOpener),
		},
		public: &FolderList{
			fs:      f,
			public:  true,
			folders: make(map[string]fileOpener),
		}}

	ctx = context.WithValue(ctx, CtxAppIDKey, f)
	logTags := make(logger.CtxLogTags)
	logTags[CtxIDKey] = CtxOpID
	ctx = logger.NewContextWithLogTags(ctx, logTags)
	f.context = ctx

	f.launchNotificationProcessor(ctx)

	return f, nil
}

var vinfo = dokan.VolumeInformation{
	VolumeName:             "KBFS",
	MaximumComponentLength: 0xFF, // This can be changed.
	FileSystemFlags: dokan.FileCasePreservedNames | dokan.FileCaseSensitiveSearch |
		dokan.FileUnicodeOnDisk | dokan.FileSupportsReparsePoints,
	FileSystemName: "KBFS",
}

// GetVolumeInformation returns information about the whole filesystem for dokan.
func (f *FS) GetVolumeInformation() (dokan.VolumeInformation, error) {
	// TODO should this be refused to other users?
	return vinfo, nil
}

// GetDiskFreeSpace returns information about free space on the volume for dokan.
func (*FS) GetDiskFreeSpace() (dokan.FreeSpace, error) {
	// TODO should this be refused to other users?
	return dokan.FreeSpace{}, nil
}

// openContext is for opening files.
type openContext struct {
	fi *dokan.FileInfo
	*dokan.CreateData
	redirectionsLeft int
}

// reduceRedictionsLeft reduces redirections and returns whether there are
// redirections left (true), or whether processing should be stopped (false).
func (oc *openContext) reduceRedirectionsLeft() bool {
	oc.redirectionsLeft--
	return oc.redirectionsLeft > 0
}

// isCreation checks the flags whether a file creation is wanted.
func (oc *openContext) isCreateDirectory() bool {
	return oc.isCreation() && oc.CreateOptions&fileDirectoryFile != 0
}

const fileDirectoryFile = 1

// isCreation checks the flags whether a file creation is wanted.
func (oc *openContext) isCreation() bool {
	switch oc.CreateDisposition {
	case dokan.FILE_SUPERSEDE, dokan.FILE_CREATE, dokan.FILE_OPEN_IF, dokan.FILE_OVERWRITE_IF:
		return true
	}
	return false
}
func (oc *openContext) isExistingError() bool {
	switch oc.CreateDisposition {
	case dokan.FILE_CREATE:
		return true
	}
	return false
}

// isTruncate checks the flags whether a file truncation is wanted.
func (oc *openContext) isTruncate() bool {
	switch oc.CreateDisposition {
	case dokan.FILE_SUPERSEDE, dokan.FILE_OVERWRITE, dokan.FILE_OVERWRITE_IF:
		return true
	}
	return false
}

// isOpenReparsePoint checks the flags whether a reparse point open is wanted.
func (oc *openContext) isOpenReparsePoint() bool {
	return oc.CreateOptions&syscall.FILE_FLAG_OPEN_REPARSE_POINT != 0
}

func (oc *openContext) mayNotBeDirectory() bool {
	return oc.CreateOptions&dokan.FILE_NON_DIRECTORY_FILE != 0
}

func newSyntheticOpenContext() *openContext {
	var oc openContext
	oc.CreateData = &dokan.CreateData{}
	oc.CreateDisposition = dokan.FILE_OPEN
	oc.redirectionsLeft = 30
	return &oc
}

// CreateFile called from dokan, may be a file or directory.
func (f *FS) CreateFile(fi *dokan.FileInfo, cd *dokan.CreateData) (dokan.File, bool, error) {
	// Only allow the current user access
	if !fi.IsRequestorUserSidEqualTo(f.currentUserSID) {
		return nil, false, dokan.ErrAccessDenied
	}
	ctx := NewContextWithOpID(f)
	return f.openRaw(ctx, fi, cd)
}

// openRaw is a wrapper between CreateFile/CreateDirectory/OpenDirectory and open
func (f *FS) openRaw(ctx context.Context, fi *dokan.FileInfo, caf *dokan.CreateData) (dokan.File, bool, error) {
	ps, err := windowsPathSplit(fi.Path())
	if err != nil {
		return nil, false, err
	}
	oc := openContext{fi: fi, CreateData: caf, redirectionsLeft: 30}
	file, isd, err := f.open(ctx, &oc, ps)
	if err != nil {
		err = errToDokan(err)
	}
	return file, isd, err
}

// open tries to open a file deferring to more specific implementations.
func (f *FS) open(ctx context.Context, oc *openContext, ps []string) (dokan.File, bool, error) {
	switch {
	case len(ps) < 1:
		return nil, false, dokan.ErrObjectNameNotFound
	case len(ps) == 1 && ps[0] == ``:
		if oc.mayNotBeDirectory() {
			return nil, true, dokan.ErrFileIsADirectory
		}
		return f.root, true, nil
	case libkbfs.ErrorFile == ps[len(ps)-1]:
		return NewErrorFile(f), false, nil
	case MetricsFileName == ps[len(ps)-1]:
		return NewMetricsFile(f), false, nil
	case PublicName == ps[0]:
		return f.root.public.open(ctx, oc, ps[1:])
	case PrivateName == ps[0]:
		return f.root.private.open(ctx, oc, ps[1:])
	}
	return nil, false, dokan.ErrObjectNameNotFound
}

// windowsPathSplit handles paths we get from Dokan.
// As a special case `` means `\`, it gets generated
// on special occasions.
func windowsPathSplit(raw string) ([]string, error) {
	if raw == `` {
		raw = `\`
	}
	if raw[0] != '\\' {
		return nil, dokan.ErrObjectNameNotFound
	}
	return strings.Split(raw[1:], `\`), nil
}

// MoveFile tries to move a file.
func (f *FS) MoveFile(source *dokan.FileInfo, targetPath string, replaceExisting bool) (err error) {
	// User checking is handled by the opening of the source file

	ctx := NewContextWithOpID(f)
	f.log.CDebugf(ctx, "FS Rename start replaceExisting=%v", replaceExisting)
	defer func() { f.reportErr(ctx, err) }()

	oc := newSyntheticOpenContext()
	src, _, err := f.openRaw(ctx, source, oc.CreateData)
	f.log.CDebugf(ctx, "FS Rename source open -> %v,%v srcType %T", src, err, src)
	if err != nil {
		return err
	}
	defer src.Cleanup(nil)

	// Source directory
	srcDirPath, err := windowsPathSplit(source.Path())
	if err != nil {
		return err
	}
	if len(srcDirPath) < 1 {
		return errors.New("Invalid source for move")
	}
	srcName := srcDirPath[len(srcDirPath)-1]
	srcDirPath = srcDirPath[0 : len(srcDirPath)-1]
	srcDir, _, err := f.open(ctx, oc, srcDirPath)
	if err != nil {
		return err
	}
	defer srcDir.Cleanup(nil)

	// Destination directory, not the destination file
	dstPath, err := windowsPathSplit(targetPath)
	if err != nil {
		return err
	}
	if len(dstPath) < 1 {
		return errors.New("Invalid destination for move")
	}
	dstDirPath := dstPath[0 : len(dstPath)-1]

	dstDir, dstIsDir, err := f.open(ctx, oc, dstDirPath)
	f.log.CDebugf(ctx, "FS Rename dest open %v -> %v,%v,%v dstType %T", dstDirPath, dstDir, dstIsDir, err, dstDir)
	if err != nil {
		return err
	}
	defer dstDir.Cleanup(nil)
	if !dstIsDir {
		return errors.New("Tried to move to a non-directory path")
	}

	fl1, ok := srcDir.(*FolderList)
	fl2, ok2 := dstDir.(*FolderList)
	if ok && ok2 && fl1 == fl2 {
		return f.folderListRename(ctx, fl1, oc, src, srcName, dstPath, replaceExisting)
	}

	srcDirD, ok := srcDir.(*Dir)
	if !ok {
		return errors.New("Parent of src not a Dir")
	}
	srcFolder := srcDirD.folder
	srcParent := srcDirD.node

	ddst, ok := dstDir.(*Dir)
	if !ok {
		return errors.New("Destination directory is not of type Dir")
	}

	switch src.(type) {
	case *Dir:
	case *File:
	default:
		return dokan.ErrAccessDenied
	}

	// here we race...
	if !replaceExisting {
		x, _, err := f.open(ctx, oc, dstPath)
		if err == nil {
			defer x.Cleanup(nil)
		}
		if !isNoSuchNameError(err) {
			f.log.CDebugf(ctx, "FS Rename target open error %T %v", err, err)
			return errors.New("Refusing to replace existing target!")
		}

	}

	if srcFolder != ddst.folder {
		return dokan.ErrAccessDenied
	}

	// overwritten node, if any, will be removed from Folder.nodes, if
	// it is there in the first place, by its Forget

	f.log.CDebugf(ctx, "FS Rename KBFSOps().Rename(ctx,%v,%v,%v,%v)", srcParent, srcName, ddst.node, dstPath[len(dstPath)-1])
	if err := srcFolder.fs.config.KBFSOps().Rename(
		ctx, srcParent, srcName, ddst.node, dstPath[len(dstPath)-1]); err != nil {
		f.log.CDebugf(ctx, "FS Rename KBFSOps().Rename FAILED %v", err)
		return err
	}

	switch x := src.(type) {
	case *Dir:
		x.parent = ddst.node
	case *File:
		x.parent = ddst.node
	}

	f.log.CDebugf(ctx, "FS Rename SUCCESS")
	return nil
}

func (f *FS) folderListRename(ctx context.Context, fl *FolderList, oc *openContext, src dokan.File, srcName string, dstPath []string, replaceExisting bool) error {
	ef, ok := src.(*EmptyFolder)
	f.log.CDebugf(ctx, "FS Rename folderlist %v", ef)
	if !ok {
		return dokan.ErrAccessDenied
	}
	dstName := dstPath[len(dstPath)-1]
	fl.mu.Lock()
	_, ok = fl.folders[dstName]
	fl.mu.Unlock()
	if !replaceExisting && ok {
		f.log.CDebugf(ctx, "FS Rename folderlist refusing to replace target")
		return dokan.ErrAccessDenied
	}
	// Perhaps create destination by opening it.
	x, _, err := f.open(ctx, oc, dstPath)
	if err == nil {
		x.Cleanup(nil)
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	_, ok = fl.folders[dstName]
	delete(fl.folders, srcName)
	if !ok {
		f.log.CDebugf(ctx, "FS Rename folderlist adding target")
		fl.folders[dstName] = ef
	}
	f.log.CDebugf(ctx, "FS Rename folderlist success")
	return nil
}

// Mounted is called from dokan on unmount.
func (f *FS) Mounted() error {
	return nil
}

func (f *FS) processNotifications(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			f.notificationMutex.Lock()
			c := f.notifications
			f.notifications = nil
			f.notificationMutex.Unlock()
			c.Close()
			for range c.Out() {
				// Drain the output queue to allow the Channel close
				// Out() and shutdown any goroutines.
				f.log.CWarningf(ctx,
					"Throwing away notification after shutdown")
			}
			return
		case i := <-f.notifications.Out():
			notifyFn, ok := i.(func())
			if !ok {
				f.log.CWarningf(ctx, "Got a bad notification function: %v", i)
				continue
			}
			notifyFn()
			f.notificationGroup.Done()
		}
	}
}

func (f *FS) queueNotification(fn func()) {
	f.notificationGroup.Add(1)
	f.notificationMutex.RLock()
	if f.notifications == nil {
		f.log.Warning("Ignoring notification, no available channel")
		return
	}
	f.notificationMutex.RUnlock()
	f.notifications.In() <- fn
}

func (f *FS) launchNotificationProcessor(ctx context.Context) {
	f.notificationMutex.Lock()
	defer f.notificationMutex.Unlock()

	// The notifications channel needs to have "infinite" capacity,
	// because otherwise we risk a deadlock between libkbfs and
	// libfuse.  The notification processor sends invalidates to the
	// kernel.  In osxfuse 3.X, the kernel can call back into userland
	// during an invalidate (a GetAttr()) call, which in turn takes
	// locks within libkbfs.  So if libkbfs ever gets blocked while
	// trying to enqueue a notification (while it is holding locks),
	// we could have a deadlock.  Yes, if there are too many
	// outstanding notifications we'll run out of memory and crash,
	// but otherwise we risk deadlock.  Which is worse?
	f.notifications = channels.NewInfiniteChannel()

	// start the notification processor
	go f.processNotifications(ctx)
}

func (f *FS) reportErr(ctx context.Context, err error) {
	if err == nil {
		f.log.CDebugf(ctx, "Request complete")
		return
	}

	f.config.Reporter().Report(libkbfs.RptE, libkbfs.WrapError{Err: err})
	// We just log the error as debug, rather than error, because it
	// might just indicate an expected error such as an ENOENT.
	//
	// TODO: Classify errors and escalate the logging level of the
	// important ones.
	f.log.CDebugf(ctx, err.Error())
}

// Root implements the fs.FS interface for FS.
func (f *FS) Root() (dokan.File, error) {
	return f.root, nil
}

// Root represents the root of the KBFS file system.
type Root struct {
	emptyFile
	private *FolderList
	public  *FolderList
}

// GetFileInformation for dokan stats.
func (r *Root) GetFileInformation(*dokan.FileInfo) (*dokan.Stat, error) {
	return defaultDirectoryInformation()
}

// FindFiles for dokan readdir.
func (r *Root) FindFiles(fi *dokan.FileInfo, callback func(*dokan.NamedStat) error) error {
	var ns dokan.NamedStat
	ns.NumberOfLinks = 1
	ns.FileAttributes = fileAttributeDirectory
	ns.Name = PrivateName
	err := callback(&ns)
	if err != nil {
		return err
	}
	ns.Name = PublicName
	err = callback(&ns)
	return err
}
