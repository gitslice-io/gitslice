//go:build linux || darwin

package fusefs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"gitslice.io/gitslice/internal/lazyfs"
)

func TestFuseHelpersSecurityAndInodeStability(t *testing.T) {
	if got := errno(context.Canceled); got != syscall.EINTR {
		t.Fatalf("errno(context.Canceled) = %v, want EINTR", got)
	}

	// openLocal must refuse to follow symlinks.
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	n := &node{m: &mount{}}
	if fh, _, e := n.openLocal(link, syscall.O_RDONLY); e == 0 || fh != nil {
		t.Fatalf("openLocal(symlink) succeeded: fh=%v errno=%v", fh, e)
	}

	// Getattr must preserve EmbeddedInode().StableAttr().Ino across renames.
	manifest, err := lazyfs.NewManifest("c1", []lazyfs.BaseEntry{
		{Path: "/b.txt", Kind: lazyfs.KindFile, Mode: lazyfs.ModeFile, Size: 5, ContentHash: "h1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	layer, err := lazyfs.Open(t.TempDir(), manifest, nil, lazyfs.Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()

	m := &mount{layer: layer}
	root := &node{m: m}
	bridge := gofs.NewNodeFS(root, &gofs.Options{})
	_ = bridge

	origIno := inodeFor("/a.txt")
	childInode := root.NewInode(context.Background(), &node{m: m}, gofs.StableAttr{Mode: syscall.S_IFREG, Ino: origIno})
	root.AddChild("b.txt", childInode, false)
	childNode := childInode.Operations().(*node)

	var attrOut fuse.AttrOut
	if e := childNode.Getattr(context.Background(), nil, &attrOut); e != 0 {
		t.Fatalf("Getattr after rename = %v", e)
	}
	if attrOut.Attr.Ino != origIno {
		t.Fatalf("Getattr Ino = %d, want stable original %d (path-based would be %d)", attrOut.Attr.Ino, origIno, inodeFor("/b.txt"))
	}
}
