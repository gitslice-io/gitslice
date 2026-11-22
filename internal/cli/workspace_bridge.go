package cli

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/workspace"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Thin adapters over internal/workspace preserving internal/cli's userError
// codes/hints and unexported helper names during incremental package extraction.

type (
	statCache       = workspace.StatCache
	statCacheEntry  = workspace.StatCacheEntry
	listingBase     = workspace.ListingBase
	workspaceIgnore = workspace.Ignore
)

const statCacheClockSlack = workspace.StatCacheClockSlack

func translateWorkspaceError(err error) error {
	if err == nil {
		return nil
	}
	var reserved *workspace.ReservedPathError
	if errors.As(err, &reserved) {
		return userError("reserved_path", reserved.Error(), "Choose a path outside .gs.")
	}
	var damaged *workspace.DamagedError
	if errors.As(err, &damaged) {
		return userError("workspace_damaged", damaged.Error(),
			"Restore .gs/slice.json, or move the files you want to keep out and run gs init <slice|account:slice> in a new empty directory.")
	}
	if errors.Is(err, workspace.ErrNotInWorkspace) {
		return userError("not_in_workspace", "not in a gitslice workspace", "Run gs init <slice|account:slice>.")
	}
	var outside *workspace.OutsideSliceError
	if errors.As(err, &outside) {
		return userError("outside_slice", outside.Error(), "Use a path under the workspace's bound slice.")
	}
	return err
}

func isSymlinkMode(mode uint32) bool                    { return workspace.IsSymlinkMode(mode) }
func cleanShellGlobalPath(value string) (string, error) { return workspace.CleanGlobalPath(value) }
func hasReservedGsSegment(p string) bool                { return workspace.HasReservedGsSegment(p) }
func ensureNotReservedGsPath(p string) error {
	return translateWorkspaceError(workspace.EnsureNotReservedGsPath(p))
}
func cleanConfinedRelPath(rel string) (string, error) {
	out, err := workspace.CleanConfinedRelPath(rel)
	return out, translateWorkspaceError(err)
}
func isSafeRelativeSymlink(absRoot, absTarget, link string) bool {
	return workspace.IsSafeRelativeSymlink(absRoot, absTarget, link)
}
func ensureConfinedDir(root, relDir string) (string, error) {
	out, err := workspace.EnsureConfinedDir(root, relDir)
	return out, translateWorkspaceError(err)
}
func mkdirAllNoSymlinksInRoot(osRoot *os.Root, osRel string) error {
	return workspace.MkdirAllNoSymlinksInRoot(osRoot, osRel)
}
func ensureNoAncestorSymlinksInRoot(osRoot *os.Root, parentRel string) error {
	return workspace.EnsureNoAncestorSymlinksInRoot(osRoot, parentRel)
}
func writeConfinedWorkspaceFile(root, rel string, mode uint32, r io.Reader) (string, int64, error) {
	target, n, err := workspace.WriteConfinedFile(root, rel, mode, r)
	return target, n, translateWorkspaceError(err)
}
func removeConfinedWorkspaceFile(root, rel string) error {
	return translateWorkspaceError(workspace.RemoveConfinedFile(root, rel))
}
func readConfinedWorkspaceFile(root, rel string) ([]byte, error) {
	data, err := workspace.ReadConfinedFile(root, rel)
	return data, translateWorkspaceError(err)
}
func inferConfiningRootForTarget(target string) (string, string, error) {
	return workspace.InferConfiningRootForTarget(target)
}

func mergeTextLines(baseData, localData, remoteData []byte) ([]byte, bool) {
	return workspace.MergeTextLines(baseData, localData, remoteData)
}

func readStatCache(root string) statCache { return workspace.ReadStatCache(root) }
func writeStatCache(root string, files map[string]statCacheEntry) {
	workspace.WriteStatCache(root, files)
}
func statCacheEntryFor(info fs.FileInfo, contentHash string) (statCacheEntry, bool) {
	return workspace.StatCacheEntryFor(info, contentHash)
}

func readCachedListing(cache *clientcache.ObjectCache, slice *corev1.Slice, commitID string) ([]*corev1.TreeEntry, bool) {
	return workspace.ReadCachedListing(cache, slice, commitID)
}
func writeCachedListing(cache *clientcache.ObjectCache, slice *corev1.Slice, commitID string, entries []*corev1.TreeEntry) {
	workspace.WriteCachedListing(cache, slice, commitID, entries)
}
func rememberListingRef(cache *clientcache.ObjectCache, ref *corev1.SliceRef, slice *corev1.Slice) {
	workspace.RememberListingRef(cache, ref, slice)
}
func newestCachedListing(cache *clientcache.ObjectCache, ref *corev1.SliceRef) (*listingBase, bool) {
	return workspace.NewestCachedListing(cache, ref)
}
func applyListingDelta(base []*corev1.TreeEntry, removed []string, changed []*corev1.TreeEntry) []*corev1.TreeEntry {
	return workspace.ApplyListingDelta(base, removed, changed)
}

func newWorkspaceIgnore(root string, ws WorkspaceConfig, tracked ...map[string]BaseSnapshotFile) *workspaceIgnore {
	return workspace.NewIgnore(root, ws, tracked...)
}

func (r Runner) backupUncommittedEdits(root string, logicalPaths []string) (string, error) {
	return workspace.BackupUncommittedEdits(root, logicalPaths)
}
