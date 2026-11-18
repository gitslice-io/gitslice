//go:build linux || darwin

package fusefs

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"gitslice.io/gitslice/internal/lazyfs"
)

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
	attr, err := n.m.layer.CreateEmptyWritable(ctx, p, createMode(mode), flags&syscall.O_EXCL != 0, flags&syscall.O_TRUNC != 0)
	if err != nil {
		return nil, nil, 0, errno(err)
	}
	fh, fuseFlags, e := n.openLocal(attr.LocalPath, flags)
	if e != 0 {
		return nil, nil, 0, e
	}
	n.m.fill(attr, p, &out.Attr)
	child := n.NewInode(ctx, &node{m: n.m}, gofs.StableAttr{Mode: typeBits(attr.Kind), Ino: inodeFor(p)})
	return child, fh, fuseFlags, 0
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
