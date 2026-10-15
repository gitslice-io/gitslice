package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/clientcache"
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
	baseFile, baseOK := base.Files[conflict.Path]
	localFile, localOK := current[conflict.Path]
	remoteFile, remoteOK := remote[conflict.Path]
	baseData, baseExists, err := r.remoteFileBytes(ctx, repo, cache, oldBaseCommitID, baseFile, baseOK)
	if err != nil {
		return err
	}
	localData, localExists, err := localWorkspaceFileBytes(localFile, localOK)
	if err != nil {
		return err
	}
	remoteData, remoteExists, err := r.remoteFileBytes(ctx, repo, cache, newBaseCommitID, remoteFile.BaseSnapshotFile, remoteOK)
	if err != nil {
		return err
	}
	if !isTextConflictSide(baseData, baseExists) || !isTextConflictSide(localData, localExists) || !isTextConflictSide(remoteData, remoteExists) {
		return nil
	}
	target := localFile.AbsPath
	if target == "" {
		rel, err := workspaceRelPath(ws, conflict.Path)
		if err != nil {
			return err
		}
		target = filepath.Join(root, filepath.FromSlash(rel))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("<<<<<<< gitslice local\n")
	b.WriteString(conflictSideText("local", localData, localExists))
	b.WriteString("||||||| gitslice base\n")
	b.WriteString(conflictSideText("base", baseData, baseExists))
	b.WriteString("=======\n")
	b.WriteString(conflictSideText("remote", remoteData, remoteExists))
	b.WriteString(">>>>>>> gitslice remote\n")
	return os.WriteFile(target, []byte(b.String()), fileModeFromSnapshot(firstNonZeroMode(localFile.Mode, remoteFile.Mode, baseFile.Mode)))
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
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
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
	return os.WriteFile(target, []byte(b.String()), 0o100644)
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
		return data, err == nil, err
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
		return data, true, err
	}
	read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: commitID, Path: file.Path})
	if grpcstatus.Code(err) == codes.NotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := cache.PutBytes(read.Data); err != nil {
		return nil, false, err
	}
	return read.Data, true, nil
}

func localWorkspaceFileBytes(file workingFile, exists bool) ([]byte, bool, error) {
	if !exists {
		return nil, false, nil
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
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
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
