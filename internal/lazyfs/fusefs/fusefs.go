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
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

func (m *mount) recordError(p string, err error) {
	m.errors.Add(1)
	m.mu.Lock()
	m.lastError = p + ": " + err.Error()
	m.mu.Unlock()
}
