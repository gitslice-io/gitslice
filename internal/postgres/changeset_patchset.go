package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
	var preCurrentPatchsetID sql.NullString
	var preStatus string
	err := s.db.QueryRowContext(ctx, `
			select current_patchset_id, status
			from changesets
			where id = $1
		`, changesetID).Scan(&preCurrentPatchsetID, &preStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if preStatus != "draft" {
		return nil, ErrConflict
	}
	if expectedCurrentPatchsetID != "" && (!preCurrentPatchsetID.Valid || preCurrentPatchsetID.String != expectedCurrentPatchsetID) {
		return nil, ErrConflict
	}
	if patchset.BaseKind == "" {
		patchset.BaseKind = "commit"
	}
	if patchset.BaseKind == "patchset" && patchset.BasePatchsetId == "" {
		patchset.BasePatchsetId = patchset.StackParentPatchsetId
	}
	if patchset.StackParentPatchsetId == "" && patchset.BaseKind == "patchset" {
		patchset.StackParentPatchsetId = patchset.BasePatchsetId
	}
	baseTreeID, err := s.baseTreeForPatchset(ctx, s.db, patchset)
	if err != nil {
		return nil, err
	}
	patchset.BaseTreeId = baseTreeID
	resultTreeID := baseTreeID
	if len(patchset.FileEdits) > 0 {
		if s.trees == nil {
			return nil, fmt.Errorf("tree store is not configured")
		}
		treeEdits, err := treeEditsFromPatchset(ctx, s.db, patchset.FileEdits)
		if err != nil {
			return nil, err
		}
		resultTreeID, err = s.trees.ApplyEdits(ctx, baseTreeID, treeEdits)
		if errors.Is(err, treestore.ErrNotFound) {
			return nil, ErrConflict
		}
		if err != nil {
			return nil, err
		}
	}
	patchset.ResultTreeId = resultTreeID

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentPatchsetID sql.NullString
	var stackID sql.NullString
	var currentNumber int64
	var status string
	var sliceID string
	err = tx.QueryRowContext(ctx, `
			select current_patchset_id, coalesce(current_patchset_number, 0), status,
			       authoring_slice_id, stack_id
			from changesets
			where id = $1
			for update
		`, changesetID).Scan(&currentPatchsetID, &currentNumber, &status, &sliceID, &stackID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "draft" {
		return nil, ErrConflict
	}
	if expectedCurrentPatchsetID != "" && (!currentPatchsetID.Valid || currentPatchsetID.String != expectedCurrentPatchsetID) {
		return nil, ErrConflict
	}
	if len(patchset.FileEdits) > 0 {
		blobs, err := getBlobsTx(ctx, tx, blobIDsForEdits(patchset.FileEdits))
		if err != nil {
			return nil, err
		}
		for _, blobID := range blobIDsForEdits(patchset.FileEdits) {
			if _, ok := blobs[blobID]; !ok {
				return nil, ErrNotFound
			}
		}
	}
	patchsetID, err := objectid.RandomID("ps")
	if err != nil {
		return nil, err
	}
	patchset.Id = patchsetID
	patchset.ChangesetId = changesetID
	patchset.Number = currentNumber + 1
	createdAt := time.Now().UTC()
	patchset.CreatedAt = formatTime(createdAt)
	if patchset.SubmitRequirements == nil || patchset.SubmitRequirements.SourceSliceDefinitionHash == "" {
		req, err := submitRequirementsForSliceTx(ctx, tx, sliceID)
		if err != nil {
			return nil, err
		}
		patchset.SubmitRequirements = req
	}
	if currentPatchsetID.Valid && len(patchset.Conflicts) == 0 {
		current, err := getPatchsetTx(ctx, tx, currentPatchsetID.String)
		if err != nil {
			return nil, err
		}
		if current.ResultTreeId == patchset.ResultTreeId && current.BaseTreeId == patchset.BaseTreeId {
			return current, nil
		}
	}
	fileEditsJSON, err := encodeJSON(patchset.FileEdits)
	if err != nil {
		return nil, err
	}
	changedJSON, err := encodeJSON(patchset.ChangedPaths)
	if err != nil {
		return nil, err
	}
	coverageJSON, err := encodeJSON(patchset.Coverage)
	if err != nil {
		return nil, err
	}
	pathBasesJSON, err := encodeJSON(patchset.PathBases)
	if err != nil {
		return nil, err
	}
	readSetJSON, err := encodeJSON(patchset.ReadSet)
	if err != nil {
		return nil, err
	}
	writeSetJSON, err := encodeJSON(patchset.WriteSet)
	if err != nil {
		return nil, err
	}
	conflictsJSON, err := encodeJSON(patchset.Conflicts)
	if err != nil {
		return nil, err
	}
	submitRequirementsJSON, err := encodeJSON(patchset.SubmitRequirements)
	if err != nil {
		return nil, err
	}
	psAttr := resolveAttributionQuery(ctx, tx, patchset.Author)
	if psAttr.ConversationID == "" && patchset.AuthoringConversationId != "" {
		psAttr.ConversationID = patchset.AuthoringConversationId
	}
	if patchset.Attribution != nil {
		explicit := storage.ActorAttributionFromProto(patchset.Attribution)
		if explicit.ActingSubjectID != "" {
			psAttr = explicit
		}
	}
	patchset.Attribution = psAttr.ToProto()
	_, err = tx.ExecContext(ctx, `
		insert into patchsets(
			id, changeset_id, number, base_commit_id, author_subject_id, file_edits,
			changed_paths, coverage, path_bases, read_set, write_set, conflicts, kind, submit_requirements,
			base_kind, base_patchset_id, base_tree_id, result_tree_id, stack_parent_patchset_id, created_at,
			authoring_conversation_id, authoring_conversation_seq,
			acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27)
	`, patchset.Id, changesetID, patchset.Number, patchset.BaseCommitId, patchset.Author,
		fileEditsJSON, changedJSON, coverageJSON, pathBasesJSON, readSetJSON, writeSetJSON, conflictsJSON, patchset.Kind, submitRequirementsJSON,
		patchset.BaseKind, nullString(patchset.BasePatchsetId), nullString(patchset.BaseTreeId), nullString(patchset.ResultTreeId), nullString(patchset.StackParentPatchsetId), createdAt,
		nullString(patchset.AuthoringConversationId), patchset.AuthoringConversationSeq,
		psAttr.ActingSubjectID, psAttr.ControllerSubjectID, psAttr.KeyID, psAttr.DaemonID, psAttr.ConversationID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		update changesets
		set current_patchset_id = $1,
		    current_patchset_number = $2,
		    affected_paths = $3,
		    submit_blocked_reason = '',
		    daemon_id = case when acting_subject_id = $5 and daemon_id = '' then $6 else daemon_id end,
		    conversation_id = case when acting_subject_id = $5 and conversation_id = '' then $7 else conversation_id end,
		    updated_at = now()
		where id = $4
	`, patchset.Id, patchset.Number, changedJSON, changesetID, psAttr.ActingSubjectID, psAttr.DaemonID, psAttr.ConversationID)
	if err != nil {
		return nil, err
	}
	if stackID.Valid {
		if err := updateStackEntryAfterPatchsetTx(ctx, tx, stackID.String, changesetID, patchset); err != nil {
			return nil, err
		}
		if err := markStaleStackChildrenTx(ctx, tx, stackID.String, changesetID, patchset.Id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return patchset, nil
}

const patchsetSelectColumns = `id, changeset_id, number, base_commit_id, author_subject_id, created_at,
		       changed_paths, file_edits, coverage, path_bases, read_set, write_set, conflicts, kind, submit_requirements,
		       base_kind, base_patchset_id, base_tree_id, result_tree_id, stack_parent_patchset_id,
		       authoring_conversation_id, authoring_conversation_seq,
		       acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id`

func (s *ChangesetStore) listPatchsets(ctx context.Context, changesetID string) ([]*corev1.Patchset, error) {
	rows, err := s.db.QueryContext(ctx, `
		select `+patchsetSelectColumns+`
		from patchsets
		where changeset_id = $1
		order by number
	`, changesetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.Patchset
	for rows.Next() {
		patchset, err := scanPatchset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, patchset)
	}
	return out, rows.Err()
}

func (s *ChangesetStore) listPatchsetsForChangesets(ctx context.Context, changesetIDs []string) (map[string][]*corev1.Patchset, error) {
	out := make(map[string][]*corev1.Patchset, len(changesetIDs))
	if len(changesetIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select `+patchsetSelectColumns+`
		from patchsets
		where changeset_id = any($1)
		order by changeset_id, number
	`, changesetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		patchset, err := scanPatchset(rows)
		if err != nil {
			return nil, err
		}
		out[patchset.ChangesetId] = append(out[patchset.ChangesetId], patchset)
	}
	return out, rows.Err()
}

func (s *ChangesetStore) PatchsetsByConversation(ctx context.Context, conversationID string) ([]*corev1.Patchset, error) {
	if conversationID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select `+patchsetSelectColumns+`
		from patchsets
		where authoring_conversation_id = $1
		order by authoring_conversation_seq
	`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.Patchset
	for rows.Next() {
		patchset, err := scanPatchset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, patchset)
	}
	return out, rows.Err()
}

type publishQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getPatchsetTx(ctx context.Context, tx *sql.Tx, patchsetID string) (*corev1.Patchset, error) {
	return getPatchset(ctx, tx, patchsetID)
}

func getPatchset(ctx context.Context, queryer publishQueryer, patchsetID string) (*corev1.Patchset, error) {
	row := queryer.QueryRowContext(ctx, `
		select `+patchsetSelectColumns+`
		from patchsets
		where id = $1
	`, patchsetID)
	return scanPatchset(row)
}

func scanPatchset(row scanner) (*corev1.Patchset, error) {
	var patchset corev1.Patchset
	var changedJSON, fileEditsJSON, coverageJSON, pathBasesJSON, readSetJSON, writeSetJSON, conflictsJSON, submitRequirementsJSON []byte
	var basePatchsetID, baseTreeID, resultTreeID, stackParentPatchsetID, authoringConversationID sql.NullString
	var createdAt time.Time
	var attr storage.ActorAttribution
	err := row.Scan(&patchset.Id, &patchset.ChangesetId, &patchset.Number, &patchset.BaseCommitId,
		&patchset.Author, &createdAt, &changedJSON, &fileEditsJSON, &coverageJSON,
		&pathBasesJSON, &readSetJSON, &writeSetJSON, &conflictsJSON, &patchset.Kind, &submitRequirementsJSON,
		&patchset.BaseKind, &basePatchsetID, &baseTreeID, &resultTreeID, &stackParentPatchsetID,
		&authoringConversationID, &patchset.AuthoringConversationSeq,
		&attr.ActingSubjectID, &attr.ControllerSubjectID, &attr.KeyID, &attr.DaemonID, &attr.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	patchset.CreatedAt = formatTime(createdAt)
	for _, item := range []struct {
		raw []byte
		dst any
	}{
		{changedJSON, &patchset.ChangedPaths},
		{fileEditsJSON, &patchset.FileEdits},
		{coverageJSON, &patchset.Coverage},
		{pathBasesJSON, &patchset.PathBases},
		{readSetJSON, &patchset.ReadSet},
		{writeSetJSON, &patchset.WriteSet},
		{conflictsJSON, &patchset.Conflicts},
		{submitRequirementsJSON, &patchset.SubmitRequirements},
	} {
		if err := decodeJSON(item.raw, item.dst); err != nil {
			return nil, err
		}
	}
	if patchset.SubmitRequirements == nil {
		patchset.SubmitRequirements = &corev1.SubmitRequirements{}
	}
	if basePatchsetID.Valid {
		patchset.BasePatchsetId = basePatchsetID.String
	}
	if baseTreeID.Valid {
		patchset.BaseTreeId = baseTreeID.String
	}
	if resultTreeID.Valid {
		patchset.ResultTreeId = resultTreeID.String
	}
	if stackParentPatchsetID.Valid {
		patchset.StackParentPatchsetId = stackParentPatchsetID.String
	}
	if authoringConversationID.Valid {
		patchset.AuthoringConversationId = authoringConversationID.String
	}
	if attr.ActingSubjectID == "" {
		attr.ActingSubjectID = patchset.Author
	}
	if attr.ConversationID == "" && patchset.AuthoringConversationId != "" {
		attr.ConversationID = patchset.AuthoringConversationId
	}
	patchset.Attribution = attr.ToProto()
	return &patchset, nil
}

func treeEditsFromPatchsetTx(ctx context.Context, tx *sql.Tx, edits []*corev1.FileEdit) ([]treestore.FileEdit, error) {
	return treeEditsFromPatchset(ctx, tx, edits)
}

func treeEditsFromPatchset(ctx context.Context, queryer publishQueryer, edits []*corev1.FileEdit) ([]treestore.FileEdit, error) {
	blobs, err := getBlobs(ctx, queryer, blobIDsForEdits(edits))
	if err != nil {
		return nil, err
	}
	out := make([]treestore.FileEdit, 0, len(edits))
	for _, edit := range edits {
		treeEdit := treestore.FileEdit{
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
			treeEdit.File = &treestore.FileEntry{
				Path:        edit.Path,
				BlobID:      blob.Id,
				ContentHash: blob.ContentHash,
				Mode:        edit.Mode,
				Size:        blob.Size,
			}
		}
		out = append(out, treeEdit)
	}
	return out, nil
}

func blobIDsForEdits(edits []*corev1.FileEdit) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, edit := range edits {
		switch edit.Op {
		case "delete", "rename", "copy", "mkdir":
			continue
		}
		if _, ok := seen[edit.BlobId]; ok {
			continue
		}
		seen[edit.BlobId] = struct{}{}
		ids = append(ids, edit.BlobId)
	}
	sort.Strings(ids)
	return ids
}

func getBlobsTx(ctx context.Context, tx *sql.Tx, blobIDs []string) (map[string]*corev1.BlobRecord, error) {
	return getBlobs(ctx, tx, blobIDs)
}

func getBlobs(ctx context.Context, queryer publishQueryer, blobIDs []string) (map[string]*corev1.BlobRecord, error) {
	blobs := make(map[string]*corev1.BlobRecord, len(blobIDs))
	if len(blobIDs) == 0 {
		return blobs, nil
	}
	rows, err := queryer.QueryContext(ctx, `
		select id, content_hash, size
		from blobs
		where id = any($1) and state = 'available'
	`, blobIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var blob corev1.BlobRecord
		if err := rows.Scan(&blob.Id, &blob.ContentHash, &blob.Size); err != nil {
			return nil, err
		}
		blobs[blob.Id] = &blob
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return blobs, nil
}
