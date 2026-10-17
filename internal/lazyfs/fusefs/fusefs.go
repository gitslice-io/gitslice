//go:build linux || darwin

// Package fusefs serves a lazy workspace (package lazyfs) as a FUSE file
// system (design/30_lazy_workspaces.md). Listings and attributes come from the
// workspace's layers with no network; a base file's content is fetched on first
// open by the Fetcher and read from the local cache after that.
//
// Phase 2 mounts read-only: the kernel refuses every write.
package fusefs

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
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

	opens   atomic.Int64
	fetches atomic.Int64
	errors  atomic.Int64

	mu        sync.Mutex
	lastError string
}

// Mount serves layer at dir until the returned server is unmounted.
func Mount(dir string, layer *lazyfs.FS, fetcher Fetcher, opts Options) (*Mounted, error) {
	m := &mount{layer: layer, fetcher: fetcher, opts: opts, started: time.Now()}
	// The base never changes under a mount in this phase, so the kernel may
	// keep what it learns.
	long := time.Hour
	negative := 10 * time.Second
	server, err := gofs.Mount(dir, &node{m: m, path: "/"}, &gofs.Options{
		MountOptions: fuse.MountOptions{
			FsName:  opts.FsName,
			Name:    "gitslice",
			Options: []string{"ro"},
			Debug:   opts.Debug,
		},
		EntryTimeout:    &long,
		AttrTimeout:     &long,
		NegativeTimeout: &negative,
		UID:             opts.UID,
		GID:             opts.GID,
	})
	if err != nil {
		return nil, err
	}
	return &Mounted{Server: server, m: m}, nil
}

type node struct {
	gofs.Inode
	m    *mount
	path string
}

var (
	_ gofs.NodeLookuper   = (*node)(nil)
	_ gofs.NodeGetattrer  = (*node)(nil)
	_ gofs.NodeReaddirer  = (*node)(nil)
	_ gofs.NodeOpener     = (*node)(nil)
	_ gofs.NodeReadlinker = (*node)(nil)
	_ gofs.NodeStatfser   = (*node)(nil)
)

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
	out.SetTimes(&m.started, &m.started, &m.started)
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
	p := childPath(n.path, name)
	attr, err := n.m.layer.Stat(p)
	if err != nil {
		return nil, errno(err)
	}
	n.m.fill(attr, p, &out.Attr)
	child := n.NewInode(ctx, &node{m: n.m, path: p}, gofs.StableAttr{Mode: typeBits(attr.Kind), Ino: inodeFor(p)})
	return child, 0
}

func (n *node) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	attr, err := n.m.layer.Stat(n.path)
	if err != nil {
		return errno(err)
	}
	n.m.fill(attr, n.path, &out.Attr)
	return 0
}

func (n *node) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	entries, err := n.m.layer.ReadDir(n.path)
	if err != nil {
		return nil, errno(err)
	}
	list := make([]fuse.DirEntry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, fuse.DirEntry{Name: entry.Name, Mode: typeBits(entry.Kind), Ino: inodeFor(childPath(n.path, entry.Name))})
	}
	return gofs.NewListDirStream(list), 0
}

func (n *node) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	target, err := n.m.layer.Readlink(n.path)
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
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		return nil, 0, syscall.EROFS
	}
	n.m.opens.Add(1)
	attr, err := n.m.layer.Stat(n.path)
	if err != nil {
		return nil, 0, errno(err)
	}
	if attr.Kind != lazyfs.KindFile {
		return nil, 0, syscall.EINVAL
	}
	if attr.Local {
		// Local edits arrive with the writable layer (phase 3).
		return nil, 0, syscall.EIO
	}
	local, fetched, err := n.m.fetcher.Fetch(ctx, n.path, attr)
	if err != nil {
		n.m.recordError(n.path, err)
		return nil, 0, errno(err)
	}
	if fetched {
		n.m.fetches.Add(1)
	}
	file, err := os.Open(local)
	if err != nil {
		n.m.recordError(n.path, err)
		return nil, 0, syscall.EIO
	}
	// Base content never changes, so the kernel may keep its pages.
	return &handle{file: file}, fuse.FOPEN_KEEP_CACHE, 0
}

func (m *mount) recordError(p string, err error) {
	m.errors.Add(1)
	m.mu.Lock()
	m.lastError = p + ": " + err.Error()
	m.mu.Unlock()
}

type handle struct {
	file *os.File
}

var (
	_ gofs.FileReader   = (*handle)(nil)
	_ gofs.FileReleaser = (*handle)(nil)
)

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
