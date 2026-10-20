package postgres

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func applyPublishPathHeadMutationsTx(ctx context.Context, tx *sql.Tx, mutations []publishPathHeadMutation) error {
	for _, mutation := range mutations {
		if mutation.DeletePath != "" {
			if err := markPathHeadDeletedRecursiveTx(ctx, tx, mutation.DeletePath, mutation.ChangesetID, mutation.PatchsetID); err != nil {
				return err
			}
			continue
		}
		if err := upsertPathHeadsTx(ctx, tx, mutation.Heads, mutation.ChangesetID, mutation.PatchsetID); err != nil {
			return err
		}
	}
	return nil
}

type pathHeadRefreshTarget struct {
	Path      string
	Recursive bool
}

func (s *ChangesetStore) pathHeadMutationsForPatchsetRoot(ctx context.Context, rootTreeID, changesetID, patchsetID string, edits []*corev1.FileEdit) ([]publishPathHeadMutation, error) {
	var mutations []publishPathHeadMutation
	var pending []PathHead
	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		mutations = append(mutations, publishPathHeadMutation{
			Heads:       append([]PathHead(nil), pending...),
			ChangesetID: changesetID,
			PatchsetID:  patchsetID,
		})
		pending = pending[:0]
	}
	for _, target := range pathHeadRefreshTargetsForEdits(edits) {
		entry, err := s.repository.getEntryFromTree(ctx, rootTreeID, target.Path)
		if errors.Is(err, ErrNotFound) {
			flushPending()
			mutations = append(mutations, publishPathHeadMutation{
				DeletePath:  target.Path,
				ChangesetID: changesetID,
				PatchsetID:  patchsetID,
			})
			continue
		}
		if err != nil {
			return nil, err
		}
		if target.Recursive && entry.Kind == "directory" {
			if err := s.appendPathHeadSubtree(ctx, rootTreeID, *entry, &pending); err != nil {
				return nil, err
			}
			continue
		}
		pending = append(pending, pathHeadFromTreeEntry(*entry))
	}
	flushPending()
	return mutations, nil
}

func (s *ChangesetStore) appendPathHeadSubtree(ctx context.Context, rootTreeID string, entry TreeEntry, heads *[]PathHead) error {
	*heads = append(*heads, pathHeadFromTreeEntry(entry))
	if entry.Kind != "directory" {
		return nil
	}
	children, err := s.repository.listDirectoryFromTree(ctx, rootTreeID, entry.Path)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := s.appendPathHeadSubtree(ctx, rootTreeID, child, heads); err != nil {
			return err
		}
	}
	return nil
}

func pathHeadRefreshTargetsForEdits(edits []*corev1.FileEdit) []pathHeadRefreshTarget {
	seen := map[string]bool{}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		switch edit.Op {
		case "mkdir":
			addPathHeadRefreshPath(seen, edit.Path, false, true)
		case "delete":
			addPathHeadRefreshPath(seen, edit.Path, true, true)
		case "rename":
			addPathHeadRefreshPath(seen, edit.OldPath, true, true)
			addPathHeadRefreshPath(seen, edit.Path, true, true)
		case "copy":
			addPathHeadRefreshPath(seen, edit.Path, true, true)
		default:
			addPathHeadRefreshPath(seen, edit.Path, false, false)
		}
	}
	out := make([]pathHeadRefreshTarget, 0, len(seen))
	for p, recursive := range seen {
		out = append(out, pathHeadRefreshTarget{Path: p, Recursive: recursive})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func addPathHeadRefreshPath(seen map[string]bool, p string, recursiveSelf, includeSelf bool) {
	p = strings.TrimRight(p, "/")
	if p == "" || p == "/" || p == "." {
		return
	}
	if includeSelf {
		seen[p] = seen[p] || recursiveSelf
	}
	for parent := path.Dir(p); parent != "" && parent != "/" && parent != "."; parent = path.Dir(parent) {
		if _, ok := seen[parent]; !ok {
			seen[parent] = false
		}
	}
}

func applyPathHeadEditsTx(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string, edits []*corev1.FileEdit) error {
	paths := make([]string, 0, len(edits)*2)
	for _, edit := range edits {
		paths = append(paths, edit.Path)
		if edit.Op == "rename" || edit.Op == "copy" {
			paths = append(paths, edit.OldPath)
		}
	}
	heads, err := getPathHeadsTx(ctx, tx, uniqueSortedStrings(paths))
	if err != nil {
		return err
	}
	blobs, err := getBlobsTx(ctx, tx, blobIDsForEdits(edits))
	if err != nil {
		return err
	}
	dirty := map[string]PathHead{}
	setHead := func(head PathHead) {
		heads[head.Path] = head
		dirty[head.Path] = head
	}
	for _, edit := range edits {
		switch edit.Op {
		case "mkdir":
			setHead(PathHead{
				Path:             edit.Path,
				Exists:           true,
				EntryFingerprint: DirectoryEntryFingerprint(treestore.EmptyRootID()),
			})
		case "delete":
			setHead(PathHead{
				Path:             edit.Path,
				Exists:           false,
				EntryFingerprint: MissingEntryFingerprint(),
			})
		case "rename":
			head, ok := heads[edit.OldPath]
			if !ok || !head.Exists {
				return ErrConflict
			}
			setHead(PathHead{
				Path:             edit.OldPath,
				Exists:           false,
				EntryFingerprint: MissingEntryFingerprint(),
			})
			head.Path = edit.Path
			setHead(head)
		case "copy":
			head, ok := heads[edit.OldPath]
			if !ok || !head.Exists {
				return ErrConflict
			}
			head.Path = edit.Path
			setHead(head)
		default:
			blob, ok := blobs[edit.BlobId]
			if !ok {
				return ErrNotFound
			}
			entry := FileEntry{
				Path:        edit.Path,
				BlobID:      blob.Id,
				ContentHash: blob.ContentHash,
				Mode:        edit.Mode,
				Size:        blob.Size,
			}
			setHead(pathHeadFromFile(entry))
		}
	}
	return upsertPathHeadsTx(ctx, tx, pathHeadsFromMap(dirty), changesetID, patchsetID)
}

func upsertPathHeadTx(ctx context.Context, tx *sql.Tx, head PathHead, changesetID, patchsetID string) error {
	return upsertPathHeadsTx(ctx, tx, []PathHead{head}, changesetID, patchsetID)
}

func upsertPathHeadsTx(ctx context.Context, tx *sql.Tx, heads []PathHead, changesetID, patchsetID string) error {
	heads = normalizedPathHeads(heads)
	if len(heads) == 0 {
		return nil
	}
	batch := newPathHeadBatch(heads)
	_, err := tx.ExecContext(ctx, `
		insert into path_heads(path, exists, entry_fingerprint, blob_id, content_hash, mode, size, accepted_changeset_id, accepted_patchset_id, updated_at)
		select input.path,
		       input.path_exists,
		       input.entry_fingerprint,
		       case when input.path_exists then input.blob_id end,
		       case when input.path_exists then input.content_hash end,
		       case when input.path_exists then input.mode::integer end,
		       case when input.path_exists then input.size end,
		       $8::text,
		       $9::text,
		       now()
		from unnest(
			$1::text[], $2::boolean[], $3::text[], $4::text[],
			$5::text[], $6::bigint[], $7::bigint[]
		) as input(path, path_exists, entry_fingerprint, blob_id, content_hash, mode, size)
		on conflict (path) do update
		set exists = excluded.exists,
		    entry_fingerprint = excluded.entry_fingerprint,
		    blob_id = excluded.blob_id,
		    content_hash = excluded.content_hash,
		    mode = excluded.mode,
		    size = excluded.size,
		    accepted_changeset_id = excluded.accepted_changeset_id,
		    accepted_patchset_id = excluded.accepted_patchset_id,
		    updated_at = now()
	`, batch.paths, batch.exists, batch.entryFingerprints, batch.blobIDs, batch.contentHashes, batch.modes, batch.sizes,
		nullString(changesetID), nullString(patchsetID))
	return err
}

func markPathHeadDeletedRecursiveTx(ctx context.Context, tx *sql.Tx, p, changesetID, patchsetID string) error {
	var acceptedChangesetID, acceptedPatchsetID any
	if changesetID != "" {
		acceptedChangesetID = changesetID
	}
	if patchsetID != "" {
		acceptedPatchsetID = patchsetID
	}
	_, err := tx.ExecContext(ctx, `
		update path_heads
		set exists = false,
		    entry_fingerprint = $1,
		    blob_id = null,
		    content_hash = null,
		    mode = null,
		    size = null,
		    accepted_changeset_id = $2,
		    accepted_patchset_id = $3,
		    updated_at = now()
		where path = $4 or path like $5
		escape '\'
	`, MissingEntryFingerprint(), acceptedChangesetID, acceptedPatchsetID, p, likePrefixPattern(p))
	if err != nil {
		return err
	}
	return upsertPathHeadTx(ctx, tx, PathHead{
		Path:             p,
		Exists:           false,
		EntryFingerprint: MissingEntryFingerprint(),
	}, changesetID, patchsetID)
}

func likePrefixPattern(p string) string {
	prefix := strings.TrimRight(p, "/") + "/"
	prefix = strings.ReplaceAll(prefix, `\`, `\\`)
	prefix = strings.ReplaceAll(prefix, `%`, `\%`)
	prefix = strings.ReplaceAll(prefix, `_`, `\_`)
	return prefix + "%"
}

func insertInitialPathHeadsTx(ctx context.Context, tx *sql.Tx, heads []PathHead) error {
	heads = normalizedPathHeads(heads)
	if len(heads) == 0 {
		return nil
	}
	batch := newPathHeadBatch(heads)
	_, err := tx.ExecContext(ctx, `
		insert into path_heads(path, exists, entry_fingerprint, blob_id, content_hash, mode, size, accepted_changeset_id, accepted_patchset_id, updated_at)
		select input.path,
		       input.path_exists,
		       input.entry_fingerprint,
		       case when input.path_exists then input.blob_id end,
		       case when input.path_exists then input.content_hash end,
		       case when input.path_exists then input.mode::integer end,
		       case when input.path_exists then input.size end,
		       null,
		       null,
		       now()
		from unnest(
			$1::text[], $2::boolean[], $3::text[], $4::text[],
			$5::text[], $6::bigint[], $7::bigint[]
		) as input(path, path_exists, entry_fingerprint, blob_id, content_hash, mode, size)
		on conflict (path) do nothing
	`, batch.paths, batch.exists, batch.entryFingerprints, batch.blobIDs, batch.contentHashes, batch.modes, batch.sizes)
	return err
}

type pathHeadBatch struct {
	paths             []string
	exists            []bool
	entryFingerprints []string
	blobIDs           []string
	contentHashes     []string
	modes             []int64
	sizes             []int64
}

func newPathHeadBatch(heads []PathHead) pathHeadBatch {
	batch := pathHeadBatch{
		paths:             make([]string, 0, len(heads)),
		exists:            make([]bool, 0, len(heads)),
		entryFingerprints: make([]string, 0, len(heads)),
		blobIDs:           make([]string, 0, len(heads)),
		contentHashes:     make([]string, 0, len(heads)),
		modes:             make([]int64, 0, len(heads)),
		sizes:             make([]int64, 0, len(heads)),
	}
	for _, head := range heads {
		batch.paths = append(batch.paths, head.Path)
		batch.exists = append(batch.exists, head.Exists)
		batch.entryFingerprints = append(batch.entryFingerprints, head.EntryFingerprint)
		batch.blobIDs = append(batch.blobIDs, head.BlobID)
		batch.contentHashes = append(batch.contentHashes, head.ContentHash)
		batch.modes = append(batch.modes, int64(head.Mode))
		batch.sizes = append(batch.sizes, head.Size)
	}
	return batch
}

func normalizedPathHeads(heads []PathHead) []PathHead {
	byPath := make(map[string]PathHead, len(heads))
	for _, head := range heads {
		byPath[head.Path] = head
	}
	return pathHeadsFromMap(byPath)
}

func pathHeadsFromMap(heads map[string]PathHead) []PathHead {
	out := make([]PathHead, 0, len(heads))
	for _, head := range heads {
		out = append(out, head)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func pathHeadFromFile(entry FileEntry) PathHead {
	return PathHead{
		Path:             entry.Path,
		Exists:           true,
		EntryFingerprint: FileEntryFingerprint(entry),
		BlobID:           entry.BlobID,
		ContentHash:      entry.ContentHash,
		Mode:             entry.Mode,
		Size:             entry.Size,
	}
}

func pathHeadFromTreeEntry(entry TreeEntry) PathHead {
	if entry.Kind == "directory" {
		return PathHead{
			Path:             entry.Path,
			Exists:           true,
			EntryFingerprint: DirectoryEntryFingerprint(entry.TreeID),
		}
	}
	return pathHeadFromFile(FileEntry{
		Path:        entry.Path,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Mode:        entry.Mode,
		Size:        entry.Size,
	})
}
