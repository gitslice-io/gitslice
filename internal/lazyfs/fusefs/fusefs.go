//go:build linux || darwin

// Package fusefs serves a lazy workspace (package lazyfs) as a FUSE file
// system (design/30_lazy_workspaces.md). Listings and attributes come from the
// workspace's layers with no network; a base file's content is fetched on first
// open by the Fetcher and read from the local cache after that.
//
// Writes go to the workspace's local layer and nowhere else; nothing here
// talks to a server except the Fetcher, which only reads. With ReadOnly, the
// kernel refuses every write.
package fusefs

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"gitslice.io/gitslice/internal/lazyfs"
)

// Fetcher makes a base file's content readable on this machine.
type Fetcher interface {
	// Fetch returns the path of a local file holding the content of the base
	// file at p, fetching it if needed (and, if it likes, files next to it).
	// fetched reports whether it had to fetch.
	Fetch(ctx context.Context, p string, attr lazyfs.Attr) (local string, fetched bool, err error)
}

// Options configure a mount.
type Options struct {
	// FsName is the source shown in the mount table, such as
	// "gitslice:acme/app".
	FsName string
	UID    uint32
	GID    uint32
	Debug  bool
	// ReadOnly mounts read-only.
	ReadOnly bool
	// GsDir, when set, is served at /.gs: the workspace state the gs CLI reads
	// and writes, kept on local disk outside the layers.
	GsDir string
	// MountTimeout is how long to wait for the kernel to bring the mount up;
	// zero means DefaultMountTimeout. A FUSE driver that is installed but not
	// allowed to load (macFUSE before its system extension is approved) makes
	// the mount call wait, not fail.
	MountTimeout time.Duration
}

// DefaultMountTimeout is long enough for a driver that asks to be approved
// and for a slow first mount, and short enough to be an answer.
const DefaultMountTimeout = 30 * time.Second

// ErrMountTimeout is a mount that did not come up in time.
var ErrMountTimeout = errors.New("the mount did not come up in time")

// mountWithin runs mount and waits at most timeout for it. A mount that comes
// up after the wait is unmounted again: nobody is serving it.
func mountWithin(timeout time.Duration, mount func() (*fuse.Server, error)) (*fuse.Server, error) {
	type outcome struct {
		server *fuse.Server
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		server, err := mount()
		done <- outcome{server, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.server, result.err
	case <-timer.C:
		go func() {
			if late := <-done; late.err == nil && late.server != nil {
				_ = late.server.Unmount()
			}
		}()
		return nil, ErrMountTimeout
	}
}

// Stats count what a mount has done.
type Stats struct {
	Opens     int64
	Fetches   int64 // opens that had to fetch content
	Errors    int64
	LastError string
}

// Mounted is a live mount.
type Mounted struct {
	Server *fuse.Server
	m      *mount
	root   *node
}

// Invalidate makes the kernel forget what it cached about p and, for a
// directory, everything under it: to call after the layers changed behind its
// back (gs restore).
func (m *Mounted) Invalidate(p string) {
	parent := m.root.EmbeddedInode()
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if p == "/" || p == "" {
		parts = nil
	}
	for i, name := range parts {
		child := parent.GetChild(name)
		if i == len(parts)-1 {
			_ = parent.NotifyEntry(name)
			if child != nil {
				_ = child.NotifyContent(0, 0)
			}
			return
		}
		if child == nil {
			return
		}
		parent = child
	}
	// The root: drop every entry under it.
	for name := range parent.Children() {
		_ = parent.NotifyEntry(name)
	}
}

// InvalidatePaths does what Invalidate does for many paths at once: after a
// sync or a switch moved the layers under the kernel.
func (m *Mounted) InvalidatePaths(paths []string) {
	for _, p := range paths {
		m.Invalidate(p)
	}
}

// Stats returns the mount's counters.
func (m *Mounted) Stats() Stats {
	m.m.mu.Lock()
	last := m.m.lastError
	m.m.mu.Unlock()
	return Stats{Opens: m.m.opens.Load(), Fetches: m.m.fetches.Load(), Errors: m.m.errors.Load(), LastError: last}
}

type mount struct {
	layer   *lazyfs.FS
	fetcher Fetcher
	opts    Options
	started time.Time
	gs      gofs.InodeEmbedder // served at /.gs, or nil

	opens   atomic.Int64
	fetches atomic.Int64
	errors  atomic.Int64

	mu        sync.Mutex
	lastError string
}

// Mount serves layer at dir until the returned server is unmounted.
func Mount(dir string, layer *lazyfs.FS, fetcher Fetcher, opts Options) (*Mounted, error) {
	m := &mount{layer: layer, fetcher: fetcher, opts: opts, started: time.Now()}
	if opts.GsDir != "" {
		gs, err := gofs.NewLoopbackRoot(opts.GsDir)
		if err != nil {
			return nil, err
		}
		m.gs = gs
	}
	// Entries and attributes are cached briefly: every change made through
	// the mount reaches the kernel anyway, and gs restore invalidates what it
	// changes behind its back.
	timeout := time.Second
	mountOptions := platformMountOptions(opts)
	if opts.ReadOnly {
		mountOptions = append(mountOptions, "ro")
	}
	root := &node{m: m}
	mountTimeout := opts.MountTimeout
	if mountTimeout <= 0 {
		mountTimeout = DefaultMountTimeout
	}
	server, err := mountWithin(mountTimeout, func() (*fuse.Server, error) {
		return gofs.Mount(dir, root, &gofs.Options{
			MountOptions: fuse.MountOptions{
				FsName:  opts.FsName,
				Name:    "gitslice",
				Options: mountOptions,
				Debug:   opts.Debug,
			},
			EntryTimeout:    &timeout,
			AttrTimeout:     &timeout,
			NegativeTimeout: &timeout,
			UID:             opts.UID,
			GID:             opts.GID,
		})
	})
	if err != nil {
		return nil, err
	}
	return &Mounted{Server: server, m: m, root: root}, nil
}

// Probe finds out whether FUSE works here: it mounts an empty folder at a
// temporary mount point and unmounts it again. It returns ErrMountTimeout when
// the mount does not come up in time, which is what a driver that is installed
// but not approved to load looks like.
func Probe(timeout time.Duration) error {
	source, err := os.MkdirTemp("", "gs-fuse-probe-src-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(source)
	point, err := os.MkdirTemp("", "gs-fuse-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(point)
	root, err := gofs.NewLoopbackRoot(source)
	if err != nil {
		return err
	}
	server, err := mountWithin(timeout, func() (*fuse.Server, error) {
		return gofs.Mount(point, root, &gofs.Options{
			MountOptions: fuse.MountOptions{FsName: "gitslice-probe", Name: "gitslice"},
		})
	})
	if err != nil {
		return err
	}
	return server.Unmount()
}

type node struct {
	gofs.Inode
	m *mount
}

// fullPath is the node's repository path, from the inode tree, which go-fuse
// keeps up to date through renames.
func (n *node) fullPath() string {
	return "/" + n.Path(nil)
}

var (
	_ gofs.NodeLookuper   = (*node)(nil)
	_ gofs.NodeGetattrer  = (*node)(nil)
	_ gofs.NodeReaddirer  = (*node)(nil)
	_ gofs.NodeOpener     = (*node)(nil)
	_ gofs.NodeReadlinker = (*node)(nil)
	_ gofs.NodeStatfser   = (*node)(nil)
	_ gofs.NodeOnAdder    = (*node)(nil)
	_ gofs.NodeCreater    = (*node)(nil)
	_ gofs.NodeMkdirer    = (*node)(nil)
	_ gofs.NodeUnlinker   = (*node)(nil)
	_ gofs.NodeRmdirer    = (*node)(nil)
	_ gofs.NodeRenamer    = (*node)(nil)
	_ gofs.NodeSymlinker  = (*node)(nil)
	_ gofs.NodeSetattrer  = (*node)(nil)
	_ gofs.NodeLinker     = (*node)(nil)
)

const gsName = ".gs"

// OnAdd puts the workspace state directory at /.gs.
func (n *node) OnAdd(ctx context.Context) {
	if !n.IsRoot() || n.m.gs == nil {
		return
	}
	n.AddChild(gsName, n.NewPersistentInode(ctx, n.m.gs, gofs.StableAttr{Mode: syscall.S_IFDIR}), false)
}

func (n *node) isGs(name string) bool {
	return name == gsName && n.m.gs != nil && n.IsRoot()
}

func childPath(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

// inode numbers are stable per path, so a tool that remembers them across
// lookups (find, tar) sees the same file.
func inodeFor(p string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(p))
	ino := h.Sum64()
	if ino < 2 { // 1 is the root
		ino += 2
	}
	return ino
}

func typeBits(kind lazyfs.Kind) uint32 {
	switch kind {
	case lazyfs.KindDir:
		return syscall.S_IFDIR
	case lazyfs.KindSymlink:
		return syscall.S_IFLNK
	}
	return syscall.S_IFREG
}

func (m *mount) fill(attr lazyfs.Attr, p string, out *fuse.Attr) {
	out.Ino = inodeFor(p)
	switch attr.Kind {
	case lazyfs.KindDir:
		out.Mode = syscall.S_IFDIR | 0o755
		out.Nlink = 2
	case lazyfs.KindSymlink:
		out.Mode = syscall.S_IFLNK | 0o777
		out.Nlink = 1
	default:
		out.Mode = syscall.S_IFREG | 0o644
		if attr.Mode&0o111 != 0 {
			out.Mode = syscall.S_IFREG | 0o755
		}
		out.Nlink = 1
	}
	out.Size = uint64(attr.Size)
	out.Blocks = (out.Size + 511) / 512
	out.Blksize = 4096
	out.Uid, out.Gid = m.opts.UID, m.opts.GID
	t := m.started
	if !attr.ModTime.IsZero() {
		t = attr.ModTime
	}
	out.SetTimes(&t, &t, &t)
}

// errno maps lazyfs and fetch errors to the codes a program expects.
func errno(err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, fs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, lazyfs.ErrNotDir):
		return syscall.ENOTDIR
	case errors.Is(err, lazyfs.ErrIsDir):
		return syscall.EISDIR
	case errors.Is(err, lazyfs.ErrNotEmpty):
		return syscall.ENOTEMPTY
	case errors.Is(err, fs.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, fs.ErrInvalid):
		return syscall.EINVAL
	case errors.Is(err, fs.ErrPermission):
		return syscall.EACCES
	}
	return syscall.EIO
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if n.isGs(name) {
		var st syscall.Stat_t
		if err := syscall.Lstat(n.m.opts.GsDir, &st); err != nil {
			return nil, gofs.ToErrno(err)
		}
		out.Attr.FromStat(&st)
		return n.GetChild(gsName), 0
	}
	p := childPath(n.fullPath(), name)
	attr, err := n.m.layer.Stat(p)
	if err != nil {
		return nil, errno(err)
	}
	n.m.fill(attr, p, &out.Attr)
	child := n.NewInode(ctx, &node{m: n.m}, gofs.StableAttr{Mode: typeBits(attr.Kind), Ino: inodeFor(p)})
	return child, 0
}

func (n *node) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	attr, err := n.m.layer.Stat(n.fullPath())
	if err != nil {
		return errno(err)
	}
	n.m.fill(attr, n.fullPath(), &out.Attr)
	return 0
}

func (n *node) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	entries, err := n.m.layer.ReadDir(n.fullPath())
	if err != nil {
		return nil, errno(err)
	}
	list := make([]fuse.DirEntry, 0, len(entries)+1)
	if n.IsRoot() && n.m.gs != nil {
		list = append(list, fuse.DirEntry{Name: gsName, Mode: syscall.S_IFDIR})
	}
	for _, entry := range entries {
		list = append(list, fuse.DirEntry{Name: entry.Name, Mode: typeBits(entry.Kind), Ino: inodeFor(childPath(n.fullPath(), entry.Name))})
	}
	return gofs.NewListDirStream(list), 0
}

func (n *node) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	target, err := n.m.layer.Readlink(n.fullPath())
	if err != nil {
		return nil, errno(err)
	}
	return []byte(target), 0
}

func (n *node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	out.Bsize = 4096
	out.NameLen = 255
	return 0
}

func (n *node) Open(ctx context.Context, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	n.m.opens.Add(1)
	attr, err := n.m.layer.Stat(n.fullPath())
	if err != nil {
		return nil, 0, errno(err)
	}
	if attr.Kind != lazyfs.KindFile {
		return nil, 0, syscall.EINVAL
	}
	writing := flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0
	if writing {
		if n.m.opts.ReadOnly {
			return nil, 0, syscall.EROFS
		}
		// The first write copies the base content up; with O_TRUNC there is
		// nothing to copy.
		local, err := n.m.layer.OpenWritable(ctx, n.fullPath(), flags&syscall.O_TRUNC != 0)
		if err != nil {
			n.m.recordError(n.fullPath(), err)
			return nil, 0, errno(err)
		}
		return n.openLocal(local, flags)
	}
	if attr.Local {
		return n.openLocal(attr.LocalPath, flags)
	}
	local, fetched, err := n.m.fetcher.Fetch(ctx, n.fullPath(), attr)
	if err != nil {
		n.m.recordError(n.fullPath(), err)
		return nil, 0, errno(err)
	}
	if fetched {
		n.m.fetches.Add(1)
	}
	file, err := os.Open(local)
	if err != nil {
		n.m.recordError(n.fullPath(), err)
		return nil, 0, syscall.EIO
	}
	// Base content changes only when gs restore or a sync swaps it, and both
	// tell the kernel; until then it may keep the pages.
	return &handle{file: file}, fuse.FOPEN_KEEP_CACHE, 0
}

// openLocal opens a local layer file for reading or writing.
func (n *node) openLocal(local string, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	mode := os.O_RDONLY
	switch flags & syscall.O_ACCMODE {
	case syscall.O_WRONLY:
		mode = os.O_WRONLY
	case syscall.O_RDWR:
		mode = os.O_RDWR
	}
	file, err := os.OpenFile(local, mode, 0)
	if err != nil {
		return nil, 0, gofs.ToErrno(err)
	}
	return &handle{file: file, local: true}, 0, 0
}

func (n *node) writable() syscall.Errno {
	if n.m.opts.ReadOnly {
		return syscall.EROFS
	}
	return 0
}

// newChild looks a child up after a change made it, for the kernel.
func (n *node) newChild(ctx context.Context, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	p := childPath(n.fullPath(), name)
	attr, err := n.m.layer.Stat(p)
	if err != nil {
		return nil, errno(err)
	}
	n.m.fill(attr, p, &out.Attr)
	return n.NewInode(ctx, &node{m: n.m}, gofs.StableAttr{Mode: typeBits(attr.Kind), Ino: inodeFor(p)}), 0
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*gofs.Inode, gofs.FileHandle, uint32, syscall.Errno) {
	if e := n.writable(); e != 0 {
		return nil, nil, 0, e
	}
	if n.isGs(name) {
		return nil, nil, 0, syscall.EEXIST
	}
	p := childPath(n.fullPath(), name)
	if _, err := n.m.layer.Stat(p); err == nil {
		if flags&syscall.O_EXCL != 0 {
			return nil, nil, 0, syscall.EEXIST
		}
	} else if err := n.m.layer.WriteFile(p, nil, createMode(mode)); err != nil {
		return nil, nil, 0, errno(err)
	}
	local, err := n.m.layer.OpenWritable(ctx, p, flags&syscall.O_TRUNC != 0)
	if err != nil {
		return nil, nil, 0, errno(err)
	}
	child, e := n.newChild(ctx, name, out)
	if e != 0 {
		return nil, nil, 0, e
	}
	fh, fuseFlags, e := n.openLocal(local, flags)
	return child, fh, fuseFlags, e
}

// createMode is the native mode of a file created with the given permissions.
func createMode(perm uint32) uint32 {
	if perm&0o111 != 0 {
		return lazyfs.ModeExecutable
	}
	return lazyfs.ModeFile
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if e := n.writable(); e != 0 {
		return nil, e
	}
	if n.isGs(name) {
		return nil, syscall.EEXIST
	}
	if err := n.m.layer.Mkdir(childPath(n.fullPath(), name)); err != nil {
		return nil, errno(err)
	}
	return n.newChild(ctx, name, out)
}

func (n *node) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if e := n.writable(); e != 0 {
		return nil, e
	}
	if n.isGs(name) {
		return nil, syscall.EEXIST
	}
	if err := n.m.layer.Symlink(target, childPath(n.fullPath(), name)); err != nil {
		return nil, errno(err)
	}
	return n.newChild(ctx, name, out)
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	if e := n.writable(); e != 0 {
		return e
	}
	if n.isGs(name) {
		return syscall.EPERM
	}
	p := childPath(n.fullPath(), name)
	attr, err := n.m.layer.Stat(p)
	if err != nil {
		return errno(err)
	}
	if attr.Kind == lazyfs.KindDir {
		return syscall.EISDIR
	}
	return errno(n.m.layer.Remove(p))
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	if e := n.writable(); e != 0 {
		return e
	}
	if n.isGs(name) {
		return syscall.EPERM
	}
	p := childPath(n.fullPath(), name)
	attr, err := n.m.layer.Stat(p)
	if err != nil {
		return errno(err)
	}
	if attr.Kind != lazyfs.KindDir {
		return syscall.ENOTDIR
	}
	return errno(n.m.layer.Remove(p))
}

// renameNoReplace and renameExchange are rename(2)'s RENAME_NOREPLACE and
// RENAME_EXCHANGE flags.
const (
	renameNoReplace = 0x1
	renameExchange  = 0x2
)

func (n *node) Rename(ctx context.Context, name string, newParent gofs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if e := n.writable(); e != 0 {
		return e
	}
	target, ok := newParent.(*node)
	if !ok || n.isGs(name) || target.isGs(newName) {
		return syscall.EXDEV // .gs is another file system
	}
	if flags&renameExchange != 0 {
		return syscall.EINVAL
	}
	oldPath, newPath := childPath(n.fullPath(), name), childPath(target.fullPath(), newName)
	if flags&renameNoReplace != 0 {
		if _, err := n.m.layer.Stat(newPath); err == nil {
			return syscall.EEXIST
		}
	}
	return errno(n.m.layer.Rename(oldPath, newPath))
}

func (n *node) Link(ctx context.Context, target gofs.InodeEmbedder, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	// The native model has no hard links.
	return nil, syscall.EPERM
}

func (n *node) Setattr(ctx context.Context, fh gofs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if size, ok := in.GetSize(); ok {
		if e := n.writable(); e != 0 {
			return e
		}
		if h, ok := fh.(*handle); ok && h.local {
			if err := h.file.Truncate(int64(size)); err != nil {
				return gofs.ToErrno(err)
			}
		} else if err := n.m.layer.Truncate(ctx, n.fullPath(), int64(size)); err != nil {
			return errno(err)
		}
	}
	if mode, ok := in.GetMode(); ok {
		if e := n.writable(); e != 0 {
			return e
		}
		if err := n.m.layer.Chmod(n.fullPath(), mode); err != nil {
			return errno(err)
		}
	}
	// Owners and times are not part of the native model: accepted, not kept
	// (an editor touching a file must not fail).
	return n.Getattr(ctx, fh, out)
}

func (m *mount) recordError(p string, err error) {
	m.errors.Add(1)
	m.mu.Lock()
	m.lastError = p + ": " + err.Error()
	m.mu.Unlock()
}

type handle struct {
	file  *os.File
	local bool // a local layer file, which may be written
}

var (
	_ gofs.FilePassthroughFder = (*handle)(nil)
	_ gofs.FileReader          = (*handle)(nil)
	_ gofs.FileWriter          = (*handle)(nil)
	_ gofs.FileFsyncer         = (*handle)(nil)
	_ gofs.FileFlusher         = (*handle)(nil)
	_ gofs.FileReleaser        = (*handle)(nil)
)

// PassthroughFd lets the kernel read and write a local layer file directly,
// at disk speed, where it supports FUSE passthrough (Linux 6.9 and later, for
// a privileged mount); go-fuse falls back to serving reads and writes itself
// when it cannot. Base content is never passed through: its file is in the
// cache all workspaces share, and the kernel keeps one backing file per inode,
// so a later open for writing would write to it.
func (h *handle) PassthroughFd() (int, bool) {
	if !h.local {
		return 0, false
	}
	return int(h.file.Fd()), true
}

func (h *handle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if !h.local {
		return 0, syscall.EBADF
	}
	n, err := h.file.WriteAt(data, off)
	if err != nil {
		return uint32(n), gofs.ToErrno(err)
	}
	return uint32(n), 0
}

func (h *handle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return gofs.ToErrno(h.file.Sync())
}

func (h *handle) Flush(ctx context.Context) syscall.Errno { return 0 }

func (h *handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n, err := h.file.ReadAt(dest, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *handle) Release(ctx context.Context) syscall.Errno {
	if err := h.file.Close(); err != nil {
		return syscall.EIO
	}
	return 0
}
