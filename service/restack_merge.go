package service

import (
	"bytes"
	"context"
	"io"

	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/proto/core/v1"
)

// restackMergeMaxBytes bounds the files a restack merges line by line; larger
// ones that changed on both sides are conflicts.
const restackMergeMaxBytes = 4 << 20

// rebaseEdits takes a patchset's edits onto a new base (design/07_conflict_resolution.md).
// An edit's path that the new base has not changed since the patchset's own
// base keeps the edit. A text file both sides changed is merged line by line:
// the merge replaces the edit, and where the changes overlap it carries
// conflict markers and the path is a conflict. Anything else both sides
// changed (a binary or large file, a delete, a move, a file added on both
// sides) is a conflict that keeps the patchset's version. Restack used to
// replay the edits as they were, so a file changed upstream was silently
// overwritten with the patchset's copy.
func (s *ChangesetStackService) rebaseEdits(ctx context.Context, cs *corev1.Changeset, patchset *corev1.Patchset, newBaseCommitID, newBaseTreeID string) ([]*corev1.FileEdit, []*corev1.PatchsetConflict, error) {
	oldBases := map[string]*corev1.PathBase{}
	for _, base := range patchset.GetPathBases() {
		if base != nil {
			oldBases[base.GetPath()] = base
		}
	}
	newBases := map[string]*corev1.PathBase{}
	changedUpstream := func(p string) (bool, error) {
		old := oldBases[p]
		if p == "" || old == nil {
			return false, nil
		}
		current, ok := newBases[p]
		if !ok {
			var err error
			if current, err = s.validator.pathBase(ctx, newBaseCommitID, newBaseTreeID, p); err != nil {
				return false, err
			}
			newBases[p] = current
		}
		return current.GetEntryFingerprint() != old.GetEntryFingerprint(), nil
	}
	conflict := func(p, class string, edit *corev1.FileEdit) *corev1.PatchsetConflict {
		out := &corev1.PatchsetConflict{
			Path:              p,
			ConflictClass:     class,
			OldBaseCommitId:   cs.GetBaseCommitId(),
			NewBaseCommitId:   newBaseCommitID,
			BaseFingerprint:   oldBases[p].GetEntryFingerprint(),
			RemoteFingerprint: newBases[p].GetEntryFingerprint(),
			BaseContentHash:   oldBases[p].GetContentHash(),
			RemoteContentHash: newBases[p].GetContentHash(),
		}
		if edit != nil && edit.GetOp() != "delete" {
			out.LocalContentHash = edit.GetContentHash()
		}
		return out
	}

	edits := make([]*corev1.FileEdit, 0, len(patchset.GetFileEdits()))
	var conflicts []*corev1.PatchsetConflict
	for _, edit := range cloneFileEdits(patchset.GetFileEdits()) {
		if edit == nil {
			continue
		}
		p := edit.GetPath()
		changed, err := changedUpstream(p)
		if err != nil {
			return nil, nil, err
		}
		switch edit.GetOp() {
		case "mkdir":
			edits = append(edits, edit)
		case "rename":
			oldChanged, err := changedUpstream(edit.GetOldPath())
			if err != nil {
				return nil, nil, err
			}
			if changed || oldChanged {
				conflicts = append(conflicts, conflict(p, "restack_rename", edit))
			}
			edits = append(edits, edit)
		case "delete":
			switch {
			case !changed:
				edits = append(edits, edit)
			case !newBases[p].GetExists():
				// Deleted upstream too: nothing left to delete.
			default:
				conflicts = append(conflicts, conflict(p, "restack_modify_delete", edit))
				edits = append(edits, edit)
			}
		default:
			if !changed {
				edits = append(edits, edit)
				continue
			}
			merged, class, err := s.mergeUpstream(ctx, cs, edit, oldBases[p], newBases[p])
			if err != nil {
				return nil, nil, err
			}
			if class != "" {
				conflicts = append(conflicts, conflict(p, class, edit))
			}
			edits = append(edits, merged)
		}
	}
	return edits, conflicts, nil
}

// mergeUpstream merges an edit of a file with the file's upstream change.
// class is empty for a clean merge, else the conflict's class; the returned
// edit is what the patchset keeps (the merge, or the patchset's own version).
func (s *ChangesetStackService) mergeUpstream(ctx context.Context, cs *corev1.Changeset, edit *corev1.FileEdit, old, upstream *corev1.PathBase) (*corev1.FileEdit, string, error) {
	if upstream.GetExists() && upstream.GetEntryKind() == "file" && upstream.GetContentHash() == edit.GetContentHash() {
		return edit, "", nil // the same change landed upstream
	}
	if !old.GetExists() || old.GetEntryKind() != "file" || !upstream.GetExists() || upstream.GetEntryKind() != "file" {
		return edit, "restack_add_delete", nil
	}
	base, ok, err := s.restackContent(ctx, old.GetContentHash())
	if err != nil || !ok {
		return edit, "restack_binary", err
	}
	ours, ok, err := s.restackContent(ctx, edit.GetContentHash())
	if err != nil || !ok {
		return edit, "restack_binary", err
	}
	theirs, ok, err := s.restackContent(ctx, upstream.GetContentHash())
	if err != nil || !ok {
		return edit, "restack_binary", err
	}
	merged, clean := diffutil.Merge3(base, ours, theirs, "changeset", "upstream")
	stored, err := (&BlobService{Auth: s.Auth, Blobs: s.Blobs, Slices: s.Slices, ObjectStore: s.ObjectStore}).
		uploadBlobs(ctx, cs.GetAuthoringSlice(), []*corev1.UploadBlobsEntry{{Data: merged}})
	if err != nil {
		return nil, "", err
	}
	out := cloneFileEdits([]*corev1.FileEdit{edit})[0]
	out.BlobId, out.ContentHash = stored[0].GetBlobId(), stored[0].GetContentHash()
	if !clean {
		return out, "restack_content", nil
	}
	return out, "", nil
}

// restackContent reads a text file's content; ok is false for a binary file
// or one too large to merge.
func (s *ChangesetStackService) restackContent(ctx context.Context, contentHash string) ([]byte, bool, error) {
	if contentHash == "" {
		return nil, false, nil
	}
	rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, false, grpcError(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, restackMergeMaxBytes+1))
	if err != nil {
		return nil, false, grpcError(err)
	}
	if len(data) > restackMergeMaxBytes || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, false, nil
	}
	return data, true, nil
}
