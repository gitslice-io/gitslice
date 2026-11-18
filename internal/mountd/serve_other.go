//go:build !linux && !darwin

package mountd

import (
	"context"
	"net"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type Env struct {
	Home                   string
	MountsRoot             string
	RuntimeDir             string
	Insecure               bool
	ObjectCache            func() (*clientcache.ObjectCache, error)
	PruneObjectCache       func()
	DialLazy               func(serverAddr string, insecure bool) (*grpc.ClientConn, error)
	FetchFiles             func(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error)
	WriteLazyWorkspaceInit func(rec Record, manifest *lazyfs.Manifest) error
}

func errUnsupported() error {
	return NewError("mount_unsupported", "gs mount needs FUSE, which this system does not have", "Use gs init for a full checkout.")
}

func MountTimeoutHint(goos string) string { return "" }

func (e Env) ServeMount(ctx context.Context, rec Record, debug bool, ready func()) error {
	return errUnsupported()
}

func (e Env) AttachViaSupervisor(rec Record) error { return errUnsupported() }

func (e Env) RunSupervisor(ctx context.Context) error { return errUnsupported() }

func ForceUnmount(dir string) error { return errUnsupported() }

// ListenMountControl has nothing to listen for without mounts.
func ListenMountControl(runtimeDir, id string) (net.Listener, func()) { return nil, func() {} }

// ServeMountControl returns at once: there is no listener without mounts.
func ServeMountControl(listener net.Listener, handle func(ControlRequest) ControlReply) {}

// PopulateReplyBaseFiles records the digest; no mount serves base files here.
func PopulateReplyBaseFiles(reply *ControlReply, manifest *lazyfs.Manifest, digest string, edits []lazyfs.Edit) {
	reply.BaseFilesDigest = digest
}
