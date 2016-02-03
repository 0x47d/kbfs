package test

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/keybase/client/go/libkb"
	keybase1 "github.com/keybase/client/go/protocol"
	"github.com/keybase/kbfs/libkbfs"
	"golang.org/x/net/context"
)

// LibKBFS implements the Engine interface for direct test harness usage of libkbfs.
type LibKBFS struct {
	// hack: hold references on behalf of the test harness
	refs map[libkbfs.Config]map[libkbfs.Node]bool
	// channels used to re-enable updates if disabled
	updateChannels map[libkbfs.Config]map[libkbfs.NodeID]chan<- struct{}
	// test object, mostly for logging
	t *testing.T
}

// Check that LibKBFS fully implements the Engine interface.
var _ Engine = (*LibKBFS)(nil)

// Name returns the name of the Engine.
func (k *LibKBFS) Name() string {
	return "libkbfs"
}

// Init implements the Engine interface.
func (k *LibKBFS) Init() {
	// Initialize reference holder and channels maps
	k.refs = make(map[libkbfs.Config]map[libkbfs.Node]bool)
	k.updateChannels =
		make(map[libkbfs.Config]map[libkbfs.NodeID]chan<- struct{})
}

func concatUserNamesToStrings2(a, b []username) []string {
	userSlice := make([]string, 0, len(a)+len(b))
	for _, u := range a {
		userSlice = append(userSlice, string(u))
	}
	for _, u := range b {
		userSlice = append(userSlice, string(u))
	}
	return userSlice
}

// InitTest implements the Engine interface.
func (k *LibKBFS) InitTest(t *testing.T, blockSize int64, blockChangeSize int64,
	writers []username, readers []username) map[string]User {
	users := concatUserNamesToStrings2(writers, readers)
	// Start a new log for this test.
	k.t = t
	k.t.Log("\n------------------------------------------")
	userMap := make(map[string]User)
	normalized := make([]libkb.NormalizedUsername, len(users))
	for i, name := range users {
		normalized[i] = libkb.NormalizedUsername(name)
	}
	// create the first user specially
	config := libkbfs.MakeTestConfigOrBust(t, normalized...)

	// Set the block sizes, if any
	if blockSize > 0 || blockChangeSize > 0 {
		if blockSize == 0 {
			blockSize = 512 * 1024
		}
		if blockChangeSize < 0 {
			panic("Can't handle negative blockChangeSize")
		}
		if blockChangeSize == 0 {
			blockChangeSize = 8 * 1024
		}
		// TODO: config option for max embed size.
		bsplit, err := libkbfs.NewBlockSplitterSimple(blockSize,
			uint64(blockChangeSize), config.Codec())
		if err != nil {
			panic(fmt.Sprintf("Couldn't make block splitter for block size %d,"+
				" blockChangeSize %d: %v", blockSize, blockChangeSize, err))
		}
		config.SetBlockSplitter(bsplit)
	}

	// TODO: pass this in from each test
	clock := libkbfs.TestClock{T: time.Time{}}
	config.SetClock(clock)
	userMap[users[0]] = config
	k.refs[config] = make(map[libkbfs.Node]bool)
	k.updateChannels[config] = make(map[libkbfs.NodeID]chan<- struct{})

	if len(normalized) == 1 {
		return userMap
	}

	// create the rest of the users as copies of the original config
	for i, name := range normalized[1:] {
		c := libkbfs.ConfigAsUser(config, name)
		c.SetClock(clock)
		userMap[users[i+1]] = c
		k.refs[c] = make(map[libkbfs.Node]bool)
		k.updateChannels[c] = make(map[libkbfs.NodeID]chan<- struct{})
	}
	return userMap
}

// GetUID implements the Engine interface.
func (k *LibKBFS) GetUID(u User) (uid keybase1.UID) {
	config, ok := u.(libkbfs.Config)
	if !ok {
		panic("passed parameter isn't a config object")
	}
	var err error
	_, uid, err = config.KBPKI().GetCurrentUserInfo(context.Background())
	if err != nil {
		panic(err.Error())
	}
	return uid
}

// GetRootDir implements the Engine interface.
func (k *LibKBFS) GetRootDir(u User, isPublic bool, writers []string, readers []string) (
	dir Node, err error) {
	config := u.(*libkbfs.ConfigLocal)
	h := libkbfs.NewTlfHandle()

	for _, writer := range writers {
		h.Writers = append(h.Writers, keybase1.UID(writer))
	}
	if isPublic {
		h.Readers = append(h.Readers, keybase1.PUBLIC_UID)
	} else {
		for _, reader := range readers {
			h.Readers = append(h.Readers, keybase1.UID(reader))
		}
	}

	sort.Sort(libkbfs.UIDList(h.Writers))
	sort.Sort(libkbfs.UIDList(h.Readers))

	ctx := context.Background()

	name := h.ToString(ctx, config)

	dir, _, err =
		config.KBFSOps().GetOrCreateRootNode(
			ctx, name, isPublic, libkbfs.MasterBranch)
	if err != nil {
		return nil, err
	}
	k.refs[config][dir.(libkbfs.Node)] = true
	return dir, nil
}

// CreateDir implements the Engine interface.
func (k *LibKBFS) CreateDir(u User, parentDir Node, name string) (dir Node, err error) {
	config := u.(*libkbfs.ConfigLocal)
	kbfsOps := config.KBFSOps()
	dir, _, err = kbfsOps.CreateDir(context.Background(), parentDir.(libkbfs.Node), name)
	if err != nil {
		return dir, err
	}
	k.refs[config][dir.(libkbfs.Node)] = true
	return dir, nil
}

// CreateFile implements the Engine interface.
func (k *LibKBFS) CreateFile(u User, parentDir Node, name string) (file Node, err error) {
	config := u.(*libkbfs.ConfigLocal)
	kbfsOps := config.KBFSOps()
	file, _, err = kbfsOps.CreateFile(context.Background(), parentDir.(libkbfs.Node), name, false)
	if err != nil {
		return file, err
	}
	k.refs[config][file.(libkbfs.Node)] = true
	return file, nil
}

// CreateLink implements the Engine interface.
func (k *LibKBFS) CreateLink(u User, parentDir Node, fromName, toPath string) (err error) {
	config := u.(*libkbfs.ConfigLocal)
	kbfsOps := config.KBFSOps()
	_, err = kbfsOps.CreateLink(context.Background(), parentDir.(libkbfs.Node), fromName, toPath)
	return err
}

// RemoveDir implements the Engine interface.
func (k *LibKBFS) RemoveDir(u User, dir Node, name string) (err error) {
	kbfsOps := u.(*libkbfs.ConfigLocal).KBFSOps()
	return kbfsOps.RemoveDir(context.Background(), dir.(libkbfs.Node), name)
}

// RemoveEntry implements the Engine interface.
func (k *LibKBFS) RemoveEntry(u User, dir Node, name string) (err error) {
	kbfsOps := u.(*libkbfs.ConfigLocal).KBFSOps()
	return kbfsOps.RemoveEntry(context.Background(), dir.(libkbfs.Node), name)
}

// Rename implements the Engine interface.
func (k *LibKBFS) Rename(u User, srcDir Node, srcName string,
	dstDir Node, dstName string) (err error) {
	kbfsOps := u.(*libkbfs.ConfigLocal).KBFSOps()
	return kbfsOps.Rename(context.Background(), srcDir.(libkbfs.Node), srcName, dstDir.(libkbfs.Node), dstName)
}

// WriteFile implements the Engine interface.
func (k *LibKBFS) WriteFile(u User, file Node, data string, off int64, sync bool) (err error) {
	kbfsOps := u.(*libkbfs.ConfigLocal).KBFSOps()
	err = kbfsOps.Write(context.Background(), file.(libkbfs.Node), []byte(data), off)
	if err != nil {
		return err
	}
	if sync {
		err = kbfsOps.Sync(context.Background(), file.(libkbfs.Node))
	}
	return err
}

// Sync implements the Engine interface.
func (k *LibKBFS) Sync(u User, file Node) (err error) {
	kbfsOps := u.(*libkbfs.ConfigLocal).KBFSOps()
	return kbfsOps.Sync(context.Background(), file.(libkbfs.Node))
}

// ReadFile implements the Engine interface.
func (k *LibKBFS) ReadFile(u User, file Node, off, len int64) (data string, err error) {
	kbfsOps := u.(*libkbfs.ConfigLocal).KBFSOps()
	buf := make([]byte, len)
	var numRead int64
	numRead, err = kbfsOps.Read(context.Background(), file.(libkbfs.Node), buf, off)
	if err != nil {
		return "", err
	}
	data = string(buf[:numRead])
	return data, nil
}

// Lookup implements the Engine interface.
func (k *LibKBFS) Lookup(u User, parentDir Node, name string) (file Node, symPath string, err error) {
	config := u.(*libkbfs.ConfigLocal)
	kbfsOps := config.KBFSOps()
	file, ei, err := kbfsOps.Lookup(context.Background(), parentDir.(libkbfs.Node), name)
	if err != nil {
		return file, symPath, err
	}
	if file != nil {
		k.refs[config][file.(libkbfs.Node)] = true
	}
	if ei.Type == libkbfs.Sym {
		symPath = ei.SymPath
	}
	return file, symPath, nil
}

// GetDirChildrenTypes implements the Engine interface.
func (k *LibKBFS) GetDirChildrenTypes(u User, parentDir Node) (childrenTypes map[string]string, err error) {
	kbfsOps := u.(*libkbfs.ConfigLocal).KBFSOps()
	var entries map[string]libkbfs.EntryInfo
	entries, err = kbfsOps.GetDirChildren(context.Background(), parentDir.(libkbfs.Node))
	if err != nil {
		return childrenTypes, err
	}
	childrenTypes = make(map[string]string)
	for name, entryInfo := range entries {
		childrenTypes[name] = entryInfo.Type.String()
	}
	return childrenTypes, nil
}

// SetEx implements the Engine interface.
func (k *LibKBFS) SetEx(u User, file Node, ex bool) (err error) {
	config := u.(*libkbfs.ConfigLocal)
	kbfsOps := config.KBFSOps()
	return kbfsOps.SetEx(context.Background(), file.(libkbfs.Node), ex)
}

// DisableUpdatesForTesting implements the Engine interface.
func (k *LibKBFS) DisableUpdatesForTesting(u User, dir Node) (err error) {
	config := u.(*libkbfs.ConfigLocal)
	d := dir.(libkbfs.Node)
	if _, ok := k.updateChannels[config][d.GetID()]; ok {
		// Updates are already disabled.
		return nil
	}
	var c chan<- struct{}
	c, err = libkbfs.DisableUpdatesForTesting(config, d.GetFolderBranch())
	if err != nil {
		return err
	}
	k.updateChannels[config][d.GetID()] = c
	// Also stop conflict resolution.
	err = libkbfs.DisableCRForTesting(config, d.GetFolderBranch())
	if err != nil {
		return err
	}
	return nil
}

// ReenableUpdates implements the Engine interface.
func (k *LibKBFS) ReenableUpdates(u User, dir Node) {
	config := u.(*libkbfs.ConfigLocal)
	d := dir.(libkbfs.Node)
	err := libkbfs.RestartCRForTesting(config, d.GetFolderBranch())
	if err != nil {
		panic(err)
	}
	if c, ok := k.updateChannels[config][d.GetID()]; ok {
		c <- struct{}{}
		close(c)
		delete(k.updateChannels[config], d.GetID())
	}
}

// SyncFromServer implements the Engine interface.
func (k *LibKBFS) SyncFromServer(u User, dir Node) (err error) {
	kbfsOps := u.(*libkbfs.ConfigLocal).KBFSOps()
	d := dir.(libkbfs.Node)
	return kbfsOps.SyncFromServer(context.Background(), d.GetFolderBranch())
}

// Shutdown implements the Engine interface.
func (k *LibKBFS) Shutdown(u User) error {
	config := u.(*libkbfs.ConfigLocal)
	// drop references
	k.refs[config] = make(map[libkbfs.Node]bool)
	delete(k.refs, config)
	// clear update channels
	k.updateChannels[config] = make(map[libkbfs.NodeID]chan<- struct{})
	delete(k.updateChannels, config)
	// shutdown
	if err := config.Shutdown(); err != nil {
		return err
	}
	return nil
}
