//go:build linux || darwin

package fusefs

import (
	"context"
	"errors"
	"hash/fnv"
	"io/fs"
	"os"
	"syscall"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"gitslice.io/gitslice/internal/lazyfs"
)

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
	m.fillWithIno(attr, inodeFor(p), out)
}

func (m *mount) fillWithIno(attr lazyfs.Attr, ino uint64, out *fuse.Attr) {
	out.Ino = ino
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
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
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
	p := n.fullPath()
	attr, err := n.m.layer.Stat(p)
	if err != nil {
		return errno(err)
	}
	n.m.fill(attr, p, &out.Attr)
	return 0
}

func (n *node) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	dirPath := n.fullPath()
	entries, err := n.m.layer.ReadDir(dirPath)
	if err != nil {
		return nil, errno(err)
	}
	list := make([]fuse.DirEntry, 0, len(entries)+1)
	if n.IsRoot() && n.m.gs != nil {
		list = append(list, fuse.DirEntry{Name: gsName, Mode: syscall.S_IFDIR})
	}
	for _, entry := range entries {
		list = append(list, fuse.DirEntry{Name: entry.Name, Mode: typeBits(entry.Kind), Ino: inodeFor(childPath(dirPath, entry.Name))})
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

// openLocal opens a local layer file for reading or writing without following
// symlinks.
func (n *node) openLocal(local string, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	mode := os.O_RDONLY
	switch flags & syscall.O_ACCMODE {
	case syscall.O_WRONLY:
		mode = os.O_WRONLY
	case syscall.O_RDWR:
		mode = os.O_RDWR
	}
	file, err := os.OpenFile(local, mode|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, gofs.ToErrno(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, gofs.ToErrno(err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, 0, syscall.EINVAL
	}
	return &handle{file: file, local: true}, 0, 0
}
