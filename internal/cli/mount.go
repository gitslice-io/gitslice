package cli

import (
	"context"
	"net"
	"path/filepath"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	"gitslice.io/gitslice/internal/mountd"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type (
	mountRecord         = mountd.Record
	mountControlRequest = mountd.ControlRequest
	mountControlReply   = mountd.ControlReply
	mountResolution     = mountd.Resolution
	mountdRequest       = mountd.SupervisorRequest
	mountdReply         = mountd.SupervisorReply
)

var (
	errInvalidMountID      = mountd.ErrInvalidID
	errMountUnreachable    = mountd.ErrUnreachable
	maxMountRuntimeDirLen  = mountd.MaxRuntimeDirLen
	isValidMountID         = mountd.IsValidID
	validateMountID        = mountd.ValidateID
	newMountID             = mountd.NewID
	prefetchEntries        = mountd.PrefetchEntries
	manifestFileCount      = mountd.ManifestFileCount
	writeManifest          = mountd.WriteManifest
	readManifest           = mountd.ReadManifest
	manifestEntries        = mountd.ManifestEntries
	lazyWorkspaceID        = mountd.LazyWorkspaceID
	multisetFileHash       = mountd.MultisetFileHash
	manifestDigest         = mountd.ManifestDigest
	editFingerprint        = mountd.EditFingerprint
	populateReplyBaseFiles = mountd.PopulateReplyBaseFiles
	serveMountControl      = mountd.ServeMountControl
	isMounted              = mountd.IsMounted
	forceUnmount           = mountd.ForceUnmount
	callMountd             = mountd.CallSupervisor
)

func (r Runner) mountsRoot() string {
	if dataHome := r.Getenv("XDG_DATA_HOME"); dataHome != "" && r.Home == "" {
		return filepath.Join(dataHome, "gitslice", "mounts")
	}
	return filepath.Join(r.homeDir(), ".local", "share", "gitslice", "mounts")
}

func (r Runner) mountRuntimeDir() string {
	return mountd.RuntimeDir(r.Home, r.homeDir(), r.Env, r.Getenv)
}

func (r Runner) mountStateDir(id string) string {
	return mountd.StateDir(r.mountsRoot(), id)
}

func (r Runner) mountGsDir(id string) string {
	return mountd.GsDir(r.mountsRoot(), id)
}

func (r Runner) mountControlPath(id string) string {
	return mountd.ControlPath(r.mountRuntimeDir(), id)
}

func (r Runner) mountSupervisorSocket() string {
	return mountd.SupervisorSocket(r.mountRuntimeDir(), r.mountsRoot())
}

func (r Runner) readMountRecord(id string) (mountRecord, error) {
	return mountd.ReadRecord(r.mountsRoot(), id)
}

func (r Runner) writeMountRecord(rec mountRecord) error {
	return mountd.WriteRecord(r.mountsRoot(), rec)
}

func (r Runner) mountRecords() ([]mountRecord, error) {
	return mountd.Records(r.mountsRoot())
}

func (r Runner) findMountRecord(selector string) (mountRecord, error) {
	return mountd.FindRecord(r.mountsRoot(), r.cwd(), selector)
}

func (r Runner) mountManifestPath(id, commitID string) string {
	return mountd.ManifestPath(r.mountsRoot(), id, commitID)
}

func (r Runner) loadMountManifest(rec mountRecord) (*lazyfs.Manifest, error) {
	return mountd.LoadManifest(r.mountsRoot(), rec)
}

func (r Runner) removeStaleManifests(id string, keep ...string) {
	mountd.RemoveStaleManifests(r.mountsRoot(), id, keep...)
}

func (r Runner) callMountDaemon(id string, req mountControlRequest) (mountControlReply, error) {
	return mountd.CallDaemon(r.mountRuntimeDir(), id, req)
}

func (r Runner) listenMountControl(id string) (net.Listener, func()) {
	return mountd.ListenMountControl(r.mountRuntimeDir(), id)
}

func (r Runner) mountEnv(ctx context.Context) mountd.Env {
	return mountd.Env{
		Home:             r.Home,
		MountsRoot:       r.mountsRoot(),
		RuntimeDir:       r.mountRuntimeDir(),
		Insecure:         r.Insecure || insecureFromContext(ctx),
		ObjectCache:      r.objectCache,
		PruneObjectCache: r.maybePruneObjectCache,
		DialLazy:         dialLazy,
		FetchFiles: func(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error) {
			cfg, err := r.readUserConfig()
			if err != nil {
				return 0, 0, err
			}
			return fetchBaseFiles(authContext(ctx, cfg), conn, slice, commitID, entries, cache)
		},
		WriteLazyWorkspaceInit: r.writeLazyWorkspaceState,
	}
}

func (r Runner) serveMount(ctx context.Context, rec mountRecord, debug bool, ready func()) error {
	return r.mountEnv(ctx).ServeMount(ctx, rec, debug, ready)
}

func (r Runner) attachViaSupervisor(rec mountRecord) error {
	return r.mountEnv(context.Background()).AttachViaSupervisor(rec)
}

func (r Runner) runMountSupervisor(ctx context.Context) error {
	return r.mountEnv(ctx).RunSupervisor(ctx)
}

func fetchBaseFiles(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error) {
	targets := make([]remoteReadTarget, 0, len(entries))
	for _, entry := range entries {
		targets = append(targets, remoteReadTarget{Path: entry.Path, ContentHash: entry.ContentHash, Size: entry.Size})
	}
	tm := NewTransferManager(corev1.NewRepositoryServiceClient(conn), corev1.NewBlobServiceClient(conn), slice, cache)
	res, err := tm.FetchFilesToCache(ctx, TransferFetchRequest{CommitID: commitID, Targets: targets, Workers: 1})
	return res.FetchedFiles, res.FetchedBytes, err
}

func fetchConcurrently(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error) {
	return mountd.FetchConcurrently(ctx, entries, func(ctx context.Context, group []lazyfs.BaseEntry) (int, int64, error) {
		return fetchBaseFiles(ctx, conn, slice, commitID, group, cache)
	})
}
