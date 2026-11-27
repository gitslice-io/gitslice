package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"gitslice.io/gitslice/internal/workspace"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func workspaceConflict(oldBaseCommitID, newBaseCommitID, p string, base BaseSnapshotFile, baseOK bool, local BaseSnapshotFile, localOK bool, remote BaseSnapshotFile, remoteOK bool) WorkspaceConflict {
	return WorkspaceConflict{
		Path:              p,
		ConflictClass:     workspaceConflictClass(baseOK, localOK, remoteOK),
		OldBaseCommitID:   oldBaseCommitID,
		NewBaseCommitID:   newBaseCommitID,
		BaseFingerprint:   workspaceFileFingerprint(base, baseOK),
		LocalFingerprint:  workspaceFileFingerprint(local, localOK),
		RemoteFingerprint: workspaceFileFingerprint(remote, remoteOK),
		BaseContentHash:   contentHashIfExists(base, baseOK),
		LocalContentHash:  contentHashIfExists(local, localOK),
		RemoteContentHash: contentHashIfExists(remote, remoteOK),
	}
}

func workspaceConflictClass(baseOK, localOK, remoteOK bool) string {
	switch {
	case !baseOK && localOK && remoteOK:
		return "add/add"
	case baseOK && !localOK && remoteOK:
		return "delete/modify-local"
	case baseOK && localOK && !remoteOK:
		return "modify/delete-remote"
	case localOK && remoteOK:
		return "content"
	default:
		return "modify/delete"
	}
}

func workspaceFileFingerprint(file BaseSnapshotFile, ok bool) string {
	if !ok {
		return "missing"
	}
	return fmt.Sprintf("%s:%o", file.ContentHash, normalizedSnapshotMode(file.Mode))
}

func contentHashIfExists(file BaseSnapshotFile, ok bool) string {
	if !ok {
		return ""
	}
	return file.ContentHash
}

func (r Runner) materializeWorkspaceConflict(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, root string, ws WorkspaceConfig, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile, conflict WorkspaceConflict) error {
	rel, err := resolveConfinedWorkspaceRel(root, ws, current, conflict.Path)
	if err != nil {
		return err
	}
	data, mode, ok, err := r.workspaceConflictContentInRoot(ctx, repo, cache, root, oldBaseCommitID, newBaseCommitID, base, current, remote, conflict)
	if err != nil || !ok {
		return err
	}
	if workspace.IsSymlinkMode(mode) {
		mode = 0o100644
	}
	_, _, err = writeConfinedWorkspaceFile(root, rel, mode, bytes.NewReader(data))
	return err
}

// workspaceConflictContent is a conflicted path's file with conflict markers
// around the three sides, and its mode. ok is false when a side is binary:
// the local file then stays as it is.
func (r Runner) workspaceConflictContent(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile, conflict WorkspaceConflict) ([]byte, uint32, bool, error) {
	return r.workspaceConflictContentInRoot(ctx, repo, cache, "", oldBaseCommitID, newBaseCommitID, base, current, remote, conflict)
}

func (r Runner) workspaceConflictContentInRoot(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, root, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile, conflict WorkspaceConflict) ([]byte, uint32, bool, error) {
	baseFile, baseOK := base.Files[conflict.Path]
	localFile, localOK := current[conflict.Path]
	remoteFile, remoteOK := remote[conflict.Path]
	baseData, baseExists, err := r.remoteFileBytes(ctx, repo, cache, oldBaseCommitID, baseFile, baseOK)
	if err != nil {
		return nil, 0, false, err
	}
	localData, localExists, err := confinedLocalWorkspaceFileBytes(root, cache, localFile, localOK)
	if err != nil {
		return nil, 0, false, err
	}
	remoteData, remoteExists, err := r.remoteFileBytes(ctx, repo, cache, newBaseCommitID, remoteFile.BaseSnapshotFile, remoteOK)
	if err != nil {
		return nil, 0, false, err
	}
	if !isTextConflictSide(baseData, baseExists) || !isTextConflictSide(localData, localExists) || !isTextConflictSide(remoteData, remoteExists) {
		return nil, 0, false, nil
	}
	var b strings.Builder
	b.WriteString("<<<<<<< gitslice local\n")
	b.WriteString(conflictSideText("local", localData, localExists))
	b.WriteString("||||||| gitslice base\n")
	b.WriteString(conflictSideText("base", baseData, baseExists))
	b.WriteString("=======\n")
	b.WriteString(conflictSideText("remote", remoteData, remoteExists))
	b.WriteString(">>>>>>> gitslice remote\n")
	return []byte(b.String()), normalizedSnapshotMode(firstNonZeroMode(localFile.Mode, remoteFile.Mode, baseFile.Mode)), true, nil
}

func (r Runner) materializeStackRestackConflicts(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, conflicts []WorkspaceConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	blob := corev1.NewBlobServiceClient(conn)
	callCtx := authContext(ctx, cfg)
	sliceRef := &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
	for _, conflict := range conflicts {
		if err := r.materializeStackRestackConflict(callCtx, blob, cache, sliceRef, root, ws, conflict); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) materializeStackRestackConflict(ctx context.Context, blob corev1.BlobServiceClient, cache *clientcache.ObjectCache, sliceRef *corev1.SliceRef, root string, ws WorkspaceConfig, conflict WorkspaceConflict) error {
	rel, err := workspaceRelPath(ws, conflict.Path)
	if err != nil {
		return err
	}
	baseText, err := r.stackRestackConflictSide(ctx, blob, cache, sliceRef, "base", conflict.BaseContentHash)
	if err != nil {
		return err
	}
	localText, err := r.stackRestackConflictSide(ctx, blob, cache, sliceRef, "local", conflict.LocalContentHash)
	if err != nil {
		return err
	}
	remoteText, err := r.stackRestackConflictSide(ctx, blob, cache, sliceRef, "remote", conflict.RemoteContentHash)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("<<<<<<< gitslice update local\n")
	b.WriteString(localText)
	b.WriteString("||||||| gitslice update base\n")
	b.WriteString(baseText)
	b.WriteString("=======\n")
	b.WriteString(remoteText)
	b.WriteString(">>>>>>> gitslice update remote\n")
	_, _, err = writeConfinedWorkspaceFile(root, rel, 0o100644, strings.NewReader(b.String()))
	return err
}

func (r Runner) stackRestackConflictSide(ctx context.Context, blob corev1.BlobServiceClient, cache *clientcache.ObjectCache, sliceRef *corev1.SliceRef, label, contentHash string) (string, error) {
	contentHash = strings.TrimSpace(contentHash)
	if contentHash == "" {
		return "(" + label + " side content was not returned)\n", nil
	}
	data, ok, err := r.readConflictContentHash(ctx, blob, cache, sliceRef, contentHash)
	if err != nil {
		return "", err
	}
	if !ok {
		return "(" + label + " side content is unavailable: " + contentHash + ")\n", nil
	}
	if !utf8.Valid(data) || bytes.Contains(data, []byte{0}) {
		return "(" + label + " side is binary: " + contentHash + ")\n", nil
	}
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text, nil
}

func (r Runner) readConflictContentHash(ctx context.Context, blob corev1.BlobServiceClient, cache *clientcache.ObjectCache, sliceRef *corev1.SliceRef, contentHash string) ([]byte, bool, error) {
	if cache.Exists(contentHash) {
		data, err := cache.Read(contentHash)
		if err != nil {
			return nil, false, err
		}
		if got := objectid.RawContentHash(data); got != contentHash {
			return nil, false, fmt.Errorf("conflict cached content hash mismatch: got %s, want %s", got, contentHash)
		}
		return data, true, nil
	}
	data, err := readBlobStreamBytes(ctx, blob, sliceRef, contentHash, 0, 0)
	if grpcstatus.Code(err) == codes.NotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	cached, err := cache.PutBytes(data)
	if err != nil {
		return nil, false, err
	}
	if cached.ContentHash != contentHash {
		return nil, false, fmt.Errorf("conflict content hash mismatch: got %s, want %s", cached.ContentHash, contentHash)
	}
	return data, true, nil
}

func (r Runner) remoteFileBytes(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, commitID string, file BaseSnapshotFile, exists bool) ([]byte, bool, error) {
	if !exists {
		return nil, false, nil
	}
	if file.ContentHash != "" && cache.Exists(file.ContentHash) {
		data, err := cache.Read(file.ContentHash)
		if err != nil {
			return nil, false, err
		}
		if got := objectid.RawContentHash(data); got != file.ContentHash {
			return nil, false, fmt.Errorf("cached content hash mismatch for %s: got %s, want %s", file.Path, got, file.ContentHash)
		}
		return data, true, nil
	}
	read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: commitID, Path: file.Path})
	if grpcstatus.Code(err) == codes.NotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	cached, err := cache.PutBytes(read.Data)
	if err != nil {
		return nil, false, err
	}
	if file.ContentHash != "" && cached.ContentHash != file.ContentHash {
		return nil, false, fmt.Errorf("remote content hash mismatch for %s: got %s, want %s", file.Path, cached.ContentHash, file.ContentHash)
	}
	return read.Data, true, nil
}

func localWorkspaceFileBytes(file workingFile, exists bool) ([]byte, bool, error) {
	if !exists {
		return nil, false, nil
	}
	if workspace.IsSymlinkMode(file.Mode) && file.AbsPath != "" {
		link, err := os.Readlink(file.AbsPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return []byte(link), true, nil
	}
	return confinedLocalWorkspaceFileBytes("", nil, file, exists)
}

func confinedLocalWorkspaceFileBytes(root string, cache *clientcache.ObjectCache, file workingFile, exists bool) ([]byte, bool, error) {
	if !exists {
		return nil, false, nil
	}
	if file.AbsPath == "" {
		if data, ok, err := readVerifiedCachedBytes(cache, file.ContentHash); err != nil || ok {
			return data, ok, err
		}
		return nil, false, nil
	}
	if root != "" {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, false, err
		}
		absLocal, err := filepath.Abs(file.AbsPath)
		if err != nil {
			return nil, false, err
		}
		rel, err := filepath.Rel(absRoot, absLocal)
		if err != nil {
			return nil, false, err
		}
		osRel, err := cleanConfinedRelPath(rel)
		if err != nil {
			return nil, false, err
		}
		osRoot, err := os.OpenRoot(absRoot)
		if err != nil {
			return nil, false, err
		}
		defer osRoot.Close()
		parentRel := filepath.Dir(osRel)
		if parentRel != "." {
			if err := workspace.EnsureNoAncestorSymlinksInRoot(osRoot, parentRel); err != nil {
				return nil, false, err
			}
		}
		info, err := osRoot.Lstat(osRel)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || workspace.IsSymlinkMode(file.Mode) {
			link, lerr := os.Readlink(absLocal)
			if errors.Is(lerr, os.ErrNotExist) {
				return nil, false, nil
			}
			if lerr != nil {
				return nil, false, lerr
			}
			return []byte(link), true, nil
		}
		f, err := osRoot.Open(osRel)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return nil, false, err
		}
		return data, true, nil
	}
	data, err := os.ReadFile(file.AbsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func isTextConflictSide(data []byte, exists bool) bool {
	if !exists {
		return true
	}
	if !utf8.Valid(data) {
		return false
	}
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

func conflictSideText(label string, data []byte, exists bool) string {
	if !exists {
		return "(" + label + " side deleted this file)\n"
	}
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

func workspaceConflictsToProto(conflicts []WorkspaceConflict) []*corev1.PatchsetConflict {
	out := make([]*corev1.PatchsetConflict, 0, len(conflicts))
	for _, conflict := range conflicts {
		out = append(out, &corev1.PatchsetConflict{
			Path:              conflict.Path,
			ConflictClass:     conflict.ConflictClass,
			OldBaseCommitId:   conflict.OldBaseCommitID,
			NewBaseCommitId:   conflict.NewBaseCommitID,
			BaseFingerprint:   conflict.BaseFingerprint,
			LocalFingerprint:  conflict.LocalFingerprint,
			RemoteFingerprint: conflict.RemoteFingerprint,
			BaseContentHash:   conflict.BaseContentHash,
			LocalContentHash:  conflict.LocalContentHash,
			RemoteContentHash: conflict.RemoteContentHash,
		})
	}
	return out
}

func workspaceConflictsFromProto(conflicts []*corev1.PatchsetConflict) []WorkspaceConflict {
	out := make([]WorkspaceConflict, 0, len(conflicts))
	for _, conflict := range conflicts {
		if conflict == nil {
			continue
		}
		out = append(out, WorkspaceConflict{
			Path:              conflict.Path,
			ConflictClass:     conflict.ConflictClass,
			OldBaseCommitID:   conflict.OldBaseCommitId,
			NewBaseCommitID:   conflict.NewBaseCommitId,
			BaseFingerprint:   conflict.BaseFingerprint,
			LocalFingerprint:  conflict.LocalFingerprint,
			RemoteFingerprint: conflict.RemoteFingerprint,
			BaseContentHash:   conflict.BaseContentHash,
			LocalContentHash:  conflict.LocalContentHash,
			RemoteContentHash: conflict.RemoteContentHash,
		})
	}
	return out
}

func workspaceConflictPaths(conflicts []WorkspaceConflict) []string {
	out := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		out = append(out, conflict.Path)
	}
	sort.Strings(out)
	return out
}

func workspaceConflictStatePaths(state WorkspaceConflictState) []string {
	return workspaceConflictPaths(state.Conflicts)
}

func (r Runner) unresolvedWorkspaceConflictPaths(ws WorkspaceConfig, state WorkspaceConflictState) ([]string, error) {
	root, err := r.workspaceRoot()
	if err != nil {
		return nil, err
	}
	var unresolved []string
	for _, conflict := range state.Conflicts {
		rel, err := workspaceRelPath(ws, conflict.Path)
		if err != nil {
			return nil, err
		}
		data, err := readConfinedWorkspaceFile(root, rel)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if hasWorkspaceConflictMarkers(data) {
			unresolved = append(unresolved, conflict.Path)
		}
	}
	sort.Strings(unresolved)
	return unresolved, nil
}

func hasWorkspaceConflictMarkers(data []byte) bool {
	text := string(data)
	return strings.Contains(text, "<<<<<<< gitslice local") ||
		strings.Contains(text, "||||||| gitslice base") ||
		strings.Contains(text, ">>>>>>> gitslice remote")
}
