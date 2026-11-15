//go:build linux || darwin

package fusefs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFuseHelpersSecurity(t *testing.T) {
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
}
