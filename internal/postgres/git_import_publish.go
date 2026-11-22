package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const gitImportPathHeadChunkSize = 2000

// PublishGitImportBatch publishes a contiguous batch of imported Git commits
// onto TargetRef in one ApplyEditChain flush and one Postgres transaction,
// recording git_import_commits atomically with the ref update.
func (s *ChangesetStore) PublishGitImportBatch(ctx context.Context, in storage.GitImportBatchInput) ([]storage.GitImportPublishedCommit, error) {
	if len(in.Commits) == 0 {
		return nil, nil
	}
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	targetRef := in.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	// Resolve all referenced blobs once across the entire batch.
	allEdits := make([]*corev1.FileEdit, 0, len(in.Commits)*4)
	for _, commit := range in.Commits {
		allEdits = append(allEdits, commit.FileEdits...)
	}
	blobs, err := getBlobs(ctx, s.db, blobIDsForEdits(allEdits))
	if err != nil {
		return nil, err
	}
	editSets := make([][]treestore.FileEdit, len(in.Commits))
	for i, commit := range in.Commits {
		treeEdits := make([]treestore.FileEdit, 0, len(commit.FileEdits))
		for _, edit := range commit.FileEdits {
			te := treestore.FileEdit{
				Op:      edit.Op,
				Path:    edit.Path,
				OldPath: edit.OldPath,
			}
			switch edit.Op {
			case "delete", "rename", "copy", "mkdir":
			default:
				blob, ok := blobs[edit.BlobId]
				if !ok {
					return nil, ErrNotFound
				}
				te.File = &treestore.FileEntry{
					Path:        edit.Path,
					BlobID:      blob.Id,
					ContentHash: blob.ContentHash,
					Mode:        edit.Mode,
					Size:        blob.Size,
				}
			}
			treeEdits = append(treeEdits, te)
		}
		editSets[i] = treeEdits
	}

	var published []storage.GitImportPublishedCommit
	for attempt := 0; attempt < submitRetryAttempts; attempt++ {
		published, err = s.publishGitImportBatchOnce(ctx, in, targetRef, editSets, allEdits)
		if err == nil {
			storage.RecordPublishBatch(len(published), nil)
			return published, nil
		}
		if (!errors.Is(err, ErrConflict) && !isRetryableSerializationError(err)) || attempt == submitRetryAttempts-1 {
			storage.RecordPublishBatch(0, err)
			return nil, err
		}
		wait := time.Duration(attempt+1) * submitRetryBaseWait
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, err
}

func (s *ChangesetStore) publishGitImportBatchOnce(
	ctx context.Context,
	in storage.GitImportBatchInput,
	targetRef string,
	editSets [][]treestore.FileEdit,
	allEdits []*corev1.FileEdit,
) ([]storage.GitImportPublishedCommit, error) {
	var originalCommitID, originalRootTreeID string
	err := s.db.QueryRowContext(ctx, `
		select ref.commit_id, commit.root_tree_id
		from refs ref
		join commits commit on commit.id = ref.commit_id
		where ref.name = $1
	`, targetRef).Scan(&originalCommitID, &originalRootTreeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	roots, err := s.trees.ApplyEditChain(ctx, originalRootTreeID, editSets)
	if errors.Is(err, treestore.ErrNotFound) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	finalRootTreeID := roots[len(roots)-1]
	pathHeadMutations, err := s.pathHeadMutationsForImportBatchRoot(ctx, finalRootTreeID, allEdits)
	if err != nil {
		return nil, err
	}

	baseTime := time.Now().UTC().Truncate(time.Microsecond)
	currentCommitID := originalCommitID
	commitInserts := make([]publishCommitInsert, 0, len(in.Commits))
	outboxPayloads := make([]commitPublishedPayload, 0, len(in.Commits))
	importRecords := make([]GitImportedCommitRecord, 0, len(in.Commits))
	published := make([]storage.GitImportPublishedCommit, 0, len(in.Commits))

	for i, spec := range in.Commits {
		rootTreeID := roots[i]
		baseCommitID := currentCommitID
		now := baseTime.Add(time.Duration(i) * time.Microsecond)
		message := storage.CommitMessage(spec.Title, spec.Description)
		if message == "" {
			message = "Import git commit " + spec.GitCommitID
		}
		commitID := objectid.CommitID(objectid.CommitObject{
			ParentIDs:    []string{currentCommitID},
			RootTreeID:   rootTreeID,
			Author:       in.SubjectID,
			Message:      message,
			CreatedAt:    now,
			ChangedPaths: spec.ChangedPaths,
		})
		parentJSON, err := encodeJSON([]string{currentCommitID})
		if err != nil {
			return nil, err
		}
		changedJSON, err := encodeJSON(spec.ChangedPaths)
		if err != nil {
			return nil, err
		}
		commitInserts = append(commitInserts, publishCommitInsert{
			ID:              commitID,
			ParentIDsJSON:   parentJSON,
			RootTreeID:      rootTreeID,
			AuthorSubjectID: in.SubjectID,
			Message:         message,
			CreatedAt:       now,
			ChangedJSON:     changedJSON,
		})
		outboxPayloads = append(outboxPayloads, commitPublishedPayload{
			TargetRef:    targetRef,
			CommitID:     commitID,
			BaseCommitID: baseCommitID,
			ChangedPaths: spec.ChangedPaths,
			FileEdits:    spec.FileEdits,
			CommittedAt:  now,
		})
		if in.ImportID != "" {
			importRecords = append(importRecords, GitImportedCommitRecord{
				ImportID:         in.ImportID,
				GitCommitID:      spec.GitCommitID,
				NativeCommitID:   commitID,
				Message:          spec.Title,
				Position:         spec.Position,
				ChangedPathCount: len(spec.FileEdits),
				AuthorName:       spec.AuthorName,
				AuthorEmail:      spec.AuthorEmail,
				AuthoredAt:       spec.AuthoredAt,
				FullMessage:      spec.FullMessage,
			})
		}
		published = append(published, storage.GitImportPublishedCommit{
			GitCommitID:    spec.GitCommitID,
			NativeCommitID: commitID,
			Message:        spec.Title,
		})
		currentCommitID = commitID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var lockedCommitID string
	err = tx.QueryRowContext(ctx, `
		select commit_id
		from refs
		where name = $1
		for no key update
	`, targetRef).Scan(&lockedCommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if lockedCommitID != originalCommitID {
		storage.RecordRefCASFailure()
		return nil, ErrConflict
	}
	if err := insertPublishedCommitsTx(ctx, tx, commitInserts); err != nil {
		return nil, err
	}
	if err := applyPublishPathHeadMutationsTx(ctx, tx, pathHeadMutations); err != nil {
		return nil, err
	}
	if err := insertCommitPublishedOutboxTx(ctx, tx, outboxPayloads); err != nil {
		return nil, err
	}
	if len(importRecords) > 0 {
		if err := recordGitImportCommitsTx(ctx, tx, in.ImportID, importRecords); err != nil {
			return nil, err
		}
	}
	res, err := tx.ExecContext(ctx, `
		update refs
		set commit_id = $1, version = version + 1, updated_at = now(), updated_by = $2
		where name = $3 and commit_id = $4
	`, currentCommitID, in.SubjectID, targetRef, originalCommitID)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		storage.RecordRefCASFailure()
		return nil, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return published, nil
}

// pathHeadMutationsForImportBatchRoot refreshes path_heads for every path
// (including both files and their parent directories) touched across a batch
// of imported commits against the batch's final root tree.
func (s *ChangesetStore) pathHeadMutationsForImportBatchRoot(ctx context.Context, rootTreeID string, edits []*corev1.FileEdit) ([]publishPathHeadMutation, error) {
	seen := map[string]bool{}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		switch edit.Op {
		case "delete":
			addImportPathHeadTarget(seen, edit.Path, true)
		case "rename":
			addImportPathHeadTarget(seen, edit.OldPath, true)
			addImportPathHeadTarget(seen, edit.Path, true)
		case "copy":
			addImportPathHeadTarget(seen, edit.Path, true)
		default:
			addImportPathHeadTarget(seen, edit.Path, false)
		}
	}
	targets := make([]pathHeadRefreshTarget, 0, len(seen))
	for p, recursive := range seen {
		targets = append(targets, pathHeadRefreshTarget{Path: p, Recursive: recursive})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })

	var mutations []publishPathHeadMutation
	var pending []PathHead
	flushPending := func() {
		for start := 0; start < len(pending); start += gitImportPathHeadChunkSize {
			end := min(start+gitImportPathHeadChunkSize, len(pending))
			mutations = append(mutations, publishPathHeadMutation{
				Heads: append([]PathHead(nil), pending[start:end]...),
			})
		}
		pending = pending[:0]
	}
	for _, target := range targets {
		entry, err := s.repository.getEntryFromTree(ctx, rootTreeID, target.Path)
		if errors.Is(err, ErrNotFound) {
			flushPending()
			mutations = append(mutations, publishPathHeadMutation{
				DeletePath: target.Path,
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

func addImportPathHeadTarget(seen map[string]bool, p string, recursive bool) {
	p = strings.TrimRight(p, "/")
	if p == "" || p == "/" || p == "." {
		return
	}
	seen[p] = seen[p] || recursive
	for parent := path.Dir(p); parent != "" && parent != "/" && parent != "."; parent = path.Dir(parent) {
		if _, ok := seen[parent]; !ok {
			seen[parent] = false
		}
	}
}

func recordGitImportCommitsTx(ctx context.Context, tx *sql.Tx, importID string, records []GitImportedCommitRecord) error {
	if len(records) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		with upserted as (
			insert into git_import_commits(
				import_id, git_commit_id, native_commit_id, message, position, changed_path_count,
				author_name, author_email, authored_at, full_message
			)
			values `)
	args := make([]any, 0, len(records)*10+3)
	for i, record := range records {
		var authoredAt any
		if record.AuthoredAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, record.AuthoredAt)
			if err != nil {
				return fmt.Errorf("%w: authored_at %q: %v", ErrInvalid, record.AuthoredAt, err)
			}
			authoredAt = parsed
		}
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base, base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9))
		args = append(args, importID, record.GitCommitID, record.NativeCommitID, record.Message, record.Position, record.ChangedPathCount,
			record.AuthorName, record.AuthorEmail, authoredAt, record.FullMessage)
	}
	last := records[len(records)-1]
	lastGitIdx := len(args) + 1
	lastNativeIdx := len(args) + 2
	importIDIdx := len(args) + 3
	args = append(args, last.GitCommitID, last.NativeCommitID, importID)
	b.WriteString(fmt.Sprintf(`
			on conflict (import_id, git_commit_id) do update
			set native_commit_id = excluded.native_commit_id,
			    message = excluded.message,
			    position = excluded.position,
			    changed_path_count = excluded.changed_path_count,
			    author_name = excluded.author_name,
			    author_email = excluded.author_email,
			    authored_at = excluded.authored_at,
			    full_message = excluded.full_message
			returning (xmax = 0) as inserted
		)
		update git_imports
		set imported_count = git_imports.imported_count + coalesce((select count(*) from upserted where inserted), 0),
		    last_git_commit_id = $%d,
		    final_native_commit_id = $%d,
		    updated_at = now()
		where git_imports.id = $%d
	`, lastGitIdx, lastNativeIdx, importIDIdx))
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}
