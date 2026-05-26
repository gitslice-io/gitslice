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

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

type ChangesetStore struct {
	db         *sql.DB
	trees      *treestore.Store
	repository *RepositoryStore
	slices     *SliceStore
}

type pathEntity struct {
	Path        string
	AccountID   string
	EntityID    string
	Kind        string
	ContentHash string
	Mode        uint32
}

type entityChange struct {
	Entity      pathEntity
	Path        string
	OldPath     string
	ChangeKind  string
	Source      string
	Confidence  int
	ContentHash string
	Mode        uint32
}

func (s *ChangesetStore) Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	if req.AuthoringSlice == nil {
		return nil, fmt.Errorf("authoring slice is required")
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = DefaultTargetRef
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		ref, err := s.repository.GetRef(ctx, targetRef)
		if err != nil {
			return nil, err
		}
		baseCommitID = ref.CommitId
	}
	slice, err := s.slices.Resolve(ctx, req.AuthoringSlice)
	if err != nil {
		return nil, err
	}
	id, err := objectid.RandomID("cs")
	if err != nil {
		return nil, err
	}
	empty, err := encodeJSON([]string{})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		insert into changesets(
			id, authoring_account, authoring_slice, authoring_slice_id, author_subject_id,
			target_ref, base_commit_id, title, description, status, affected_paths,
			current_patchset_number, created_at, updated_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'draft', $10, 0, $11, $11)
	`, id, req.AuthoringSlice.Account, req.AuthoringSlice.Slice, slice.Id, subjectID,
		targetRef, baseCommitID, req.Title, req.Description, empty, now)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *ChangesetStore) Get(ctx context.Context, changesetID string) (*corev1.Changeset, error) {
	var cs corev1.Changeset
	var account, slice, currentPatchsetID, commitID, pendingPublishID sql.NullString
	var affectedJSON []byte
	err := s.db.QueryRowContext(ctx, `
		select c.id, c.authoring_account, c.authoring_slice, c.author_subject_id, c.target_ref,
		       c.base_commit_id, c.title, c.description, c.status, c.affected_paths,
		       coalesce(c.current_patchset_number, 0), c.current_patchset_id,
		       c.commit_id, p.id
		from changesets c
		left join pending_publish p on p.changeset_id = c.id
		where c.id = $1
	`, changesetID).Scan(&cs.Id, &account, &slice, &cs.Author, &cs.TargetRef,
		&cs.BaseCommitId, &cs.Title, &cs.Description, &cs.Status, &affectedJSON,
		&cs.CurrentPatchsetNumber, &currentPatchsetID, &commitID, &pendingPublishID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if account.Valid || slice.Valid {
		cs.AuthoringSlice = &corev1.SliceRef{Account: account.String, Slice: slice.String}
	}
	if currentPatchsetID.Valid {
		cs.CurrentPatchsetId = currentPatchsetID.String
	}
	if commitID.Valid {
		cs.CommitId = commitID.String
	}
	if pendingPublishID.Valid {
		cs.PendingPublishId = pendingPublishID.String
	}
	if err := decodeJSON(affectedJSON, &cs.AffectedPaths); err != nil {
		return nil, err
	}
	patchsets, err := s.listPatchsets(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	cs.Patchsets = patchsets
	cs.SubmitRequirements = &corev1.SubmitRequirements{}
	return &cs, nil
}

func (s *ChangesetStore) List(ctx context.Context, req *corev1.ListChangesetsRequest) ([]*corev1.Changeset, error) {
	limit := int(req.Limit)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	status := ""
	if req.Status != "" {
		status = req.Status
	}
	if req.AuthoringSlice != nil && status != "" {
		rows, err = s.db.QueryContext(ctx, `
			select id
			from changesets
			where authoring_account = $1 and authoring_slice = $2 and status = $3
			order by updated_at desc, id desc
			limit $4
		`, req.AuthoringSlice.Account, req.AuthoringSlice.Slice, status, limit)
	} else if req.AuthoringSlice != nil {
		rows, err = s.db.QueryContext(ctx, `
			select id
			from changesets
			where authoring_account = $1 and authoring_slice = $2
			order by updated_at desc, id desc
			limit $3
		`, req.AuthoringSlice.Account, req.AuthoringSlice.Slice, limit)
	} else if status != "" {
		rows, err = s.db.QueryContext(ctx, `
			select id
			from changesets
			where status = $1
			order by updated_at desc, id desc
			limit $2
		`, status, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			select id
			from changesets
			order by updated_at desc, id desc
			limit $1
		`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*corev1.Changeset, 0, len(ids))
	for _, id := range ids {
		cs, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	return out, nil
}

func (s *ChangesetStore) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentPatchsetID sql.NullString
	var currentNumber int64
	var status string
	err = tx.QueryRowContext(ctx, `
		select current_patchset_id, coalesce(current_patchset_number, 0), status
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&currentPatchsetID, &currentNumber, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == "abandoned" || status == "submitted" {
		return nil, ErrConflict
	}
	if expectedCurrentPatchsetID != "" && (!currentPatchsetID.Valid || currentPatchsetID.String != expectedCurrentPatchsetID) {
		return nil, ErrConflict
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
	_, err = tx.ExecContext(ctx, `
		insert into patchsets(
			id, changeset_id, number, base_commit_id, author_subject_id, file_edits,
			changed_paths, coverage, path_bases, read_set, write_set, created_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, patchset.Id, changesetID, patchset.Number, patchset.BaseCommitId, patchset.Author,
		fileEditsJSON, changedJSON, coverageJSON, pathBasesJSON, readSetJSON, writeSetJSON, createdAt)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		update changesets
		set current_patchset_id = $1,
		    current_patchset_number = $2,
		    affected_paths = $3,
		    updated_at = now()
		where id = $4
	`, patchset.Id, patchset.Number, changedJSON, changesetID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return patchset, nil
}

func (s *ChangesetStore) Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var cs struct {
		ID                string
		Status            string
		CurrentPatchsetID string
		TargetRef         string
		BaseCommitID      string
		Author            string
		Title             string
		CommitID          sql.NullString
	}
	err = tx.QueryRowContext(ctx, `
		select id, status, coalesce(current_patchset_id, ''), target_ref,
		       base_commit_id, author_subject_id, title, commit_id
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&cs.ID, &cs.Status, &cs.CurrentPatchsetID, &cs.TargetRef,
		&cs.BaseCommitID, &cs.Author, &cs.Title, &cs.CommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cs.Status == "abandoned" {
		return nil, ErrConflict
	}
	if cs.Status == "submitted" && cs.CommitID.Valid {
		return &corev1.SubmitChangesetResponse{CommitId: cs.CommitID.String, TargetRef: cs.TargetRef, NewRefCommitId: cs.CommitID.String, Status: "submitted"}, nil
	}
	if cs.Status == "pending_publish" {
		var pendingID string
		err := tx.QueryRowContext(ctx, `
			select id
			from pending_publish
			where changeset_id = $1 and status = 'pending'
		`, cs.ID).Scan(&pendingID)
		if err == nil {
			return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: "pending_publish", PendingPublishId: pendingID}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, ErrConflict
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetID != expectedCurrentPatchsetID {
		return nil, ErrConflict
	}
	if cs.CurrentPatchsetID == "" {
		return nil, ErrConflict
	}
	patchset, err := getPatchsetTx(ctx, tx, cs.CurrentPatchsetID)
	if err != nil {
		return nil, err
	}
	var currentCommitID string
	err = tx.QueryRowContext(ctx, `
		select commit_id
		from refs
		where name = $1
	`, cs.TargetRef).Scan(&currentCommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	for _, base := range patchset.PathBases {
		if err := s.validateAcceptedPathBaseTx(ctx, tx, currentCommitID, base); err != nil {
			return nil, ErrConflict
		}
	}
	if err := applyPathHeadEditsTx(ctx, tx, cs.ID, patchset.Id, patchset.FileEdits); err != nil {
		return nil, err
	}
	pendingID, err := objectid.RandomID("pub")
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		insert into pending_publish(id, changeset_id, patchset_id, target_ref, base_ref_commit_id, status, created_at, updated_at)
		values ($1, $2, $3, $4, $5, 'pending', now(), now())
	`, pendingID, cs.ID, patchset.Id, cs.TargetRef, currentCommitID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		update changesets
		set status = 'pending_publish', updated_at = now()
		where id = $1
	`, cs.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: "pending_publish", PendingPublishId: pendingID}, nil
}

func (s *ChangesetStore) PublishPending(ctx context.Context, limit int) (int, error) {
	if s.trees == nil {
		return 0, fmt.Errorf("tree store is not configured")
	}
	if limit <= 0 {
		limit = 64
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		select id, changeset_id, patchset_id, target_ref
		from pending_publish
		where status = 'pending'
		order by sequence
		limit $1
		for update skip locked
	`, limit)
	if err != nil {
		return 0, err
	}
	var pending []pendingPublishRow
	for rows.Next() {
		var row pendingPublishRow
		if err := rows.Scan(&row.ID, &row.ChangesetID, &row.PatchsetID, &row.TargetRef); err != nil {
			_ = rows.Close()
			return 0, err
		}
		pending = append(pending, row)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	targetRef := pending[0].TargetRef
	batch := pending[:0]
	for _, row := range pending {
		if row.TargetRef == targetRef {
			batch = append(batch, row)
		}
	}
	var originalCommitID string
	err = tx.QueryRowContext(ctx, `
		select commit_id
		from refs
		where name = $1
		for update
	`, targetRef).Scan(&originalCommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	currentCommitID := originalCommitID
	rootTreeID, err := s.repository.rootTreeIDForCommitTx(ctx, tx, currentCommitID)
	if err != nil {
		return 0, err
	}
	baseTime := time.Now().UTC().Truncate(time.Microsecond)
	published := 0
	updatedBy := "publisher"
	for _, row := range batch {
		var cs struct {
			Author string
			Title  string
			Status string
		}
		err := tx.QueryRowContext(ctx, `
			select author_subject_id, title, status
			from changesets
			where id = $1
			for update
		`, row.ChangesetID).Scan(&cs.Author, &cs.Title, &cs.Status)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, err
		}
		if cs.Status != "pending_publish" {
			continue
		}
		patchset, err := getPatchsetTx(ctx, tx, row.PatchsetID)
		if err != nil {
			return 0, err
		}
		baseCommitID := currentCommitID
		treeEdits, err := treeEditsFromPatchsetTx(ctx, tx, patchset.FileEdits)
		if err != nil {
			return 0, err
		}
		rootTreeID, err = s.trees.ApplyEdits(ctx, rootTreeID, treeEdits)
		if errors.Is(err, treestore.ErrNotFound) {
			return 0, ErrConflict
		}
		if err != nil {
			return 0, err
		}
		now := baseTime.Add(time.Duration(published) * time.Microsecond)
		message := cs.Title
		if message == "" {
			message = "Submit " + row.ChangesetID
		}
		commitID := objectid.CommitID(objectid.CommitObject{
			ParentIDs:    []string{currentCommitID},
			RootTreeID:   rootTreeID,
			Author:       cs.Author,
			Message:      message,
			CreatedAt:    now,
			ChangedPaths: patchset.ChangedPaths,
		})
		parentJSON, err := encodeJSON([]string{currentCommitID})
		if err != nil {
			return 0, err
		}
		changedJSON, err := encodeJSON(patchset.ChangedPaths)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `
			insert into commits(id, parent_ids, root_tree_id, author_subject_id, message, created_at, changed_paths)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (id) do nothing
		`, commitID, parentJSON, rootTreeID, cs.Author, message, now, changedJSON)
		if err != nil {
			return 0, err
		}
		if err := insertCommitChangedPathsTx(ctx, tx, targetRef, commitID, patchset.ChangedPaths, now); err != nil {
			return 0, err
		}
		if err := s.refreshPathHeadsForCommitTx(ctx, tx, commitID, row.ChangesetID, row.PatchsetID, patchset.ChangedPaths); err != nil {
			return 0, err
		}
		if err := s.applyEntityHistoryTx(ctx, tx, targetRef, baseCommitID, commitID, patchset.FileEdits, now); err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `
			update pending_publish
			set status = 'published', commit_id = $1, updated_at = now(), published_at = now()
			where id = $2
		`, commitID, row.ID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `
			update changesets
			set status = 'submitted', commit_id = $1, updated_at = now()
			where id = $2
		`, commitID, row.ChangesetID)
		if err != nil {
			return 0, err
		}
		currentCommitID = commitID
		updatedBy = cs.Author
		published++
	}
	if published == 0 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	res, err := tx.ExecContext(ctx, `
		update refs
		set commit_id = $1, version = version + 1, updated_at = now(), updated_by = $2
		where name = $3 and commit_id = $4
	`, currentCommitID, updatedBy, targetRef, originalCommitID)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return published, nil
}

func insertCommitChangedPathsTx(ctx context.Context, tx *sql.Tx, targetRef, commitID string, changedPaths []string, committedAt time.Time) error {
	for _, p := range changedPaths {
		if _, err := tx.ExecContext(ctx, `
			insert into commit_changed_paths(target_ref, commit_id, path, committed_at)
			values ($1, $2, $3, $4)
			on conflict do nothing
		`, targetRef, commitID, p, committedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *ChangesetStore) refreshPathHeadsForCommitTx(ctx context.Context, tx *sql.Tx, commitID, changesetID, patchsetID string, changedPaths []string) error {
	for _, p := range pathHeadRefreshPaths(changedPaths) {
		entry, err := s.repository.getEntryAtCommitTx(ctx, tx, commitID, p)
		if errors.Is(err, ErrNotFound) {
			if err := markPathHeadDeletedRecursiveTx(ctx, tx, p, changesetID, patchsetID); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := upsertPathHeadTx(ctx, tx, pathHeadFromTreeEntry(*entry), changesetID, patchsetID); err != nil {
			return err
		}
	}
	return nil
}

func pathHeadRefreshPaths(changedPaths []string) []string {
	seen := map[string]struct{}{}
	for _, p := range changedPaths {
		p = strings.TrimRight(p, "/")
		for p != "" && p != "/" && p != "." {
			seen[p] = struct{}{}
			p = path.Dir(p)
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *ChangesetStore) applyEntityHistoryTx(ctx context.Context, tx *sql.Tx, targetRef, baseCommitID, commitID string, edits []*corev1.FileEdit, committedAt time.Time) error {
	inferredMoves, err := s.inferExactMovesTx(ctx, tx, targetRef, baseCommitID, edits)
	if err != nil {
		return err
	}
	matchedOld := map[string]struct{}{}
	matchedNew := map[string]struct{}{}
	for _, move := range inferredMoves {
		matchedOld[move.OldPath] = struct{}{}
		matchedNew[move.Path] = struct{}{}
		if err := s.moveEntityTx(ctx, tx, targetRef, move.Entity, move.OldPath, move.Path, commitID, committedAt, "exact_content_match", move.ContentHash, move.Mode); err != nil {
			return err
		}
	}
	for _, edit := range edits {
		switch edit.Op {
		case "rename":
			entity, err := s.ensurePathEntityTx(ctx, tx, targetRef, baseCommitID, edit.OldPath)
			if err != nil {
				return err
			}
			if entity == nil {
				return ErrConflict
			}
			if err := s.moveEntityTx(ctx, tx, targetRef, *entity, edit.OldPath, edit.Path, commitID, committedAt, "explicit", entity.ContentHash, entity.Mode); err != nil {
				return err
			}
		case "mkdir":
			entity, err := s.ensurePathEntityTx(ctx, tx, targetRef, baseCommitID, edit.Path)
			if err != nil {
				return err
			}
			if entity != nil {
				continue
			}
			created, err := s.createEntityTx(ctx, tx, commitID, edit.Path, "directory", "", 0)
			if err != nil {
				return err
			}
			if err := upsertCurrentPathEntityTx(ctx, tx, targetRef, *created); err != nil {
				return err
			}
			if err := insertEntityChangeTx(ctx, tx, targetRef, commitID, entityChange{Entity: *created, Path: edit.Path, ChangeKind: "added", Source: "explicit", Confidence: 100}, committedAt); err != nil {
				return err
			}
		case "delete":
			if _, ok := matchedOld[edit.Path]; ok {
				continue
			}
			entity, err := s.ensurePathEntityTx(ctx, tx, targetRef, baseCommitID, edit.Path)
			if err != nil {
				return err
			}
			if entity == nil {
				continue
			}
			if err := deleteCurrentPathEntityTx(ctx, tx, targetRef, edit.Path, entity.Kind == "directory"); err != nil {
				return err
			}
			if err := markEntityDeletedTx(ctx, tx, *entity, commitID); err != nil {
				return err
			}
			if err := insertEntityChangeTx(ctx, tx, targetRef, commitID, entityChange{Entity: *entity, Path: edit.Path, ChangeKind: "deleted", Source: "explicit", Confidence: 100, ContentHash: entity.ContentHash, Mode: entity.Mode}, committedAt); err != nil {
				return err
			}
		default:
			if _, ok := matchedNew[edit.Path]; ok {
				continue
			}
			entity, err := s.ensurePathEntityTx(ctx, tx, targetRef, baseCommitID, edit.Path)
			if err != nil {
				return err
			}
			changeKind := "modified"
			var current pathEntity
			if entity == nil {
				changeKind = "added"
				created, err := s.createEntityTx(ctx, tx, commitID, edit.Path, "file", edit.ContentHash, edit.Mode)
				if err != nil {
					return err
				}
				current = *created
			} else {
				current = *entity
				current.ContentHash = edit.ContentHash
				current.Mode = edit.Mode
			}
			if err := upsertCurrentPathEntityTx(ctx, tx, targetRef, current); err != nil {
				return err
			}
			if err := insertEntityChangeTx(ctx, tx, targetRef, commitID, entityChange{Entity: current, Path: edit.Path, ChangeKind: changeKind, Source: "explicit", Confidence: 100, ContentHash: edit.ContentHash, Mode: edit.Mode}, committedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

type inferredMove struct {
	OldPath     string
	Path        string
	Entity      pathEntity
	ContentHash string
	Mode        uint32
}

func (s *ChangesetStore) inferExactMovesTx(ctx context.Context, tx *sql.Tx, targetRef, baseCommitID string, edits []*corev1.FileEdit) ([]inferredMove, error) {
	type deleteCandidate struct {
		path   string
		entity pathEntity
	}
	type upsertCandidate struct {
		path        string
		contentHash string
		mode        uint32
	}
	deletesByKey := map[string][]deleteCandidate{}
	upsertsByKey := map[string][]upsertCandidate{}
	for _, edit := range edits {
		switch edit.Op {
		case "delete":
			entity, err := s.ensurePathEntityTx(ctx, tx, targetRef, baseCommitID, edit.Path)
			if err != nil {
				return nil, err
			}
			if entity == nil || entity.Kind != "file" || entity.ContentHash == "" {
				continue
			}
			key := exactMoveKey(entity.ContentHash, entity.Mode)
			deletesByKey[key] = append(deletesByKey[key], deleteCandidate{path: edit.Path, entity: *entity})
		case "upsert", "add", "update":
			if edit.ContentHash == "" {
				continue
			}
			entity, err := s.ensurePathEntityTx(ctx, tx, targetRef, baseCommitID, edit.Path)
			if err != nil {
				return nil, err
			}
			if entity != nil {
				continue
			}
			mode := edit.Mode
			if mode == 0 {
				mode = 0o100644
			}
			key := exactMoveKey(edit.ContentHash, mode)
			upsertsByKey[key] = append(upsertsByKey[key], upsertCandidate{path: edit.Path, contentHash: edit.ContentHash, mode: mode})
		}
	}
	var out []inferredMove
	for key, deletes := range deletesByKey {
		upserts := upsertsByKey[key]
		if len(deletes) != 1 || len(upserts) != 1 {
			continue
		}
		out = append(out, inferredMove{
			OldPath:     deletes[0].path,
			Path:        upserts[0].path,
			Entity:      deletes[0].entity,
			ContentHash: upserts[0].contentHash,
			Mode:        upserts[0].mode,
		})
	}
	return out, nil
}

func exactMoveKey(contentHash string, mode uint32) string {
	return contentHash + "\x00" + fmt.Sprint(mode)
}

func (s *ChangesetStore) moveEntityTx(ctx context.Context, tx *sql.Tx, targetRef string, entity pathEntity, oldPath, newPath, commitID string, committedAt time.Time, source, contentHash string, mode uint32) error {
	entity.Path = newPath
	if contentHash != "" {
		entity.ContentHash = contentHash
	}
	if mode != 0 {
		entity.Mode = mode
	}
	if err := moveCurrentPathEntityTx(ctx, tx, targetRef, oldPath, newPath, entity); err != nil {
		return err
	}
	return insertEntityChangeTx(ctx, tx, targetRef, commitID, entityChange{
		Entity:      entity,
		Path:        newPath,
		OldPath:     oldPath,
		ChangeKind:  "moved",
		Source:      source,
		Confidence:  100,
		ContentHash: entity.ContentHash,
		Mode:        entity.Mode,
	}, committedAt)
}

func (s *ChangesetStore) ensurePathEntityTx(ctx context.Context, tx *sql.Tx, targetRef, baseCommitID, p string) (*pathEntity, error) {
	entity, err := getCurrentPathEntityTx(ctx, tx, targetRef, p)
	if err == nil {
		return entity, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	entry, err := s.repository.getEntryAtCommitTx(ctx, tx, baseCommitID, p)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	kind := entry.Kind
	contentHash := ""
	mode := uint32(0)
	if kind == "file" {
		contentHash = entry.ContentHash
		mode = entry.Mode
	}
	entity, err = s.createEntityTx(ctx, tx, "", p, kind, contentHash, mode)
	if err != nil {
		return nil, err
	}
	if err := upsertCurrentPathEntityTx(ctx, tx, targetRef, *entity); err != nil {
		return nil, err
	}
	return entity, nil
}

func (s *ChangesetStore) createEntityTx(ctx context.Context, tx *sql.Tx, commitID, p, kind, contentHash string, mode uint32) (*pathEntity, error) {
	accountID, err := accountIDForPathTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	entityID, err := objectid.RandomID("ent")
	if err != nil {
		return nil, err
	}
	var created any
	if commitID != "" {
		created = commitID
	}
	_, err = tx.ExecContext(ctx, `
		insert into fs_entities(account_id, entity_id, kind, created_commit_id)
		values ($1, $2, $3, $4)
		on conflict do nothing
	`, accountID, entityID, kind, created)
	if err != nil {
		return nil, err
	}
	return &pathEntity{Path: p, AccountID: accountID, EntityID: entityID, Kind: kind, ContentHash: contentHash, Mode: mode}, nil
}

func accountIDForPathTx(ctx context.Context, tx *sql.Tx, p string) (string, error) {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return "", ErrInvalid
	}
	slug := strings.Split(trimmed, "/")[0]
	var accountID string
	err := tx.QueryRowContext(ctx, `
		select id
		from accounts
		where slug = $1
	`, slug).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return accountID, err
}

func getCurrentPathEntityTx(ctx context.Context, tx *sql.Tx, targetRef, p string) (*pathEntity, error) {
	var entity pathEntity
	var contentHash sql.NullString
	var mode sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		select path, account_id, entity_id, kind, content_hash, mode
		from current_path_entities
		where target_ref = $1 and path = $2
	`, targetRef, p).Scan(&entity.Path, &entity.AccountID, &entity.EntityID, &entity.Kind, &contentHash, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	entity.ContentHash = contentHash.String
	if mode.Valid && mode.Int64 > 0 {
		entity.Mode = uint32(mode.Int64)
	}
	return &entity, nil
}

func upsertCurrentPathEntityTx(ctx context.Context, tx *sql.Tx, targetRef string, entity pathEntity) error {
	var mode any
	if entity.Mode != 0 {
		mode = int(entity.Mode)
	}
	var contentHash any
	if entity.ContentHash != "" {
		contentHash = entity.ContentHash
	}
	_, err := tx.ExecContext(ctx, `
		insert into current_path_entities(target_ref, path, account_id, entity_id, kind, content_hash, mode, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, now())
		on conflict (target_ref, path) do update
		set account_id = excluded.account_id,
		    entity_id = excluded.entity_id,
		    kind = excluded.kind,
		    content_hash = excluded.content_hash,
		    mode = excluded.mode,
		    updated_at = now()
	`, targetRef, entity.Path, entity.AccountID, entity.EntityID, entity.Kind, contentHash, mode)
	return err
}

func deleteCurrentPathEntityTx(ctx context.Context, tx *sql.Tx, targetRef, p string, recursive bool) error {
	if recursive {
		_, err := tx.ExecContext(ctx, `
			delete from current_path_entities
			where target_ref = $1
			  and (path = $2 or left(path, length($3)) = $3)
		`, targetRef, p, strings.TrimRight(p, "/")+"/")
		return err
	}
	_, err := tx.ExecContext(ctx, `
		delete from current_path_entities
		where target_ref = $1 and path = $2
	`, targetRef, p)
	return err
}

func moveCurrentPathEntityTx(ctx context.Context, tx *sql.Tx, targetRef, oldPath, newPath string, entity pathEntity) error {
	if entity.Kind == "directory" {
		_, err := tx.ExecContext(ctx, `
			update current_path_entities
			set path = $2 || substring(path from length($3) + 1),
			    updated_at = now()
			where target_ref = $1
			  and path <> $3
			  and left(path, length($4)) = $4
		`, targetRef, newPath, oldPath, strings.TrimRight(oldPath, "/")+"/")
		if err != nil {
			return err
		}
	}
	if err := deleteCurrentPathEntityTx(ctx, tx, targetRef, oldPath, false); err != nil {
		return err
	}
	return upsertCurrentPathEntityTx(ctx, tx, targetRef, entity)
}

func markEntityDeletedTx(ctx context.Context, tx *sql.Tx, entity pathEntity, commitID string) error {
	_, err := tx.ExecContext(ctx, `
		update fs_entities
		set deleted_commit_id = $3
		where account_id = $1 and entity_id = $2
	`, entity.AccountID, entity.EntityID, commitID)
	return err
}

func insertEntityChangeTx(ctx context.Context, tx *sql.Tx, targetRef, commitID string, change entityChange, committedAt time.Time) error {
	source := change.Source
	if source == "" {
		source = "explicit"
	}
	confidence := change.Confidence
	if confidence == 0 {
		confidence = 100
	}
	var oldPath any
	if change.OldPath != "" {
		oldPath = change.OldPath
	}
	var contentHash any
	if change.ContentHash != "" {
		contentHash = change.ContentHash
	}
	var mode any
	if change.Mode != 0 {
		mode = int(change.Mode)
	}
	_, err := tx.ExecContext(ctx, `
		insert into commit_entity_changes(
			target_ref, commit_id, account_id, entity_id, kind, path, old_path,
			change_kind, source, confidence, content_hash, mode, committed_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		on conflict do nothing
	`, targetRef, commitID, change.Entity.AccountID, change.Entity.EntityID, change.Entity.Kind, change.Path, oldPath, change.ChangeKind, source, confidence, contentHash, mode, committedAt)
	return err
}

func (s *ChangesetStore) Abandon(ctx context.Context, changesetID string) error {
	res, err := s.db.ExecContext(ctx, `
		update changesets
		set status = 'abandoned', updated_at = now()
		where id = $1 and status <> 'submitted'
	`, changesetID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrConflict
	}
	return nil
}

func (s *ChangesetStore) listPatchsets(ctx context.Context, changesetID string) ([]*corev1.Patchset, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, changeset_id, number, base_commit_id, author_subject_id, created_at,
		       changed_paths, file_edits, coverage, path_bases, read_set, write_set
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

func getPatchsetTx(ctx context.Context, tx *sql.Tx, patchsetID string) (*corev1.Patchset, error) {
	row := tx.QueryRowContext(ctx, `
		select id, changeset_id, number, base_commit_id, author_subject_id, created_at,
		       changed_paths, file_edits, coverage, path_bases, read_set, write_set
		from patchsets
		where id = $1
	`, patchsetID)
	return scanPatchset(row)
}

func scanPatchset(row scanner) (*corev1.Patchset, error) {
	var patchset corev1.Patchset
	var changedJSON, fileEditsJSON, coverageJSON, pathBasesJSON, readSetJSON, writeSetJSON []byte
	var createdAt time.Time
	err := row.Scan(&patchset.Id, &patchset.ChangesetId, &patchset.Number, &patchset.BaseCommitId,
		&patchset.Author, &createdAt, &changedJSON, &fileEditsJSON, &coverageJSON,
		&pathBasesJSON, &readSetJSON, &writeSetJSON)
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
	} {
		if err := decodeJSON(item.raw, item.dst); err != nil {
			return nil, err
		}
	}
	patchset.SubmitRequirements = &corev1.SubmitRequirements{}
	return &patchset, nil
}

func treeEditsFromPatchsetTx(ctx context.Context, tx *sql.Tx, edits []*corev1.FileEdit) ([]treestore.FileEdit, error) {
	out := make([]treestore.FileEdit, 0, len(edits))
	for _, edit := range edits {
		treeEdit := treestore.FileEdit{
			Op:      edit.Op,
			Path:    edit.Path,
			OldPath: edit.OldPath,
		}
		switch edit.Op {
		case "delete", "rename", "mkdir":
		default:
			blob, err := getBlobTx(ctx, tx, edit.BlobId)
			if err != nil {
				return nil, err
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

func (s *ChangesetStore) validateAcceptedPathBaseTx(ctx context.Context, tx *sql.Tx, currentCommitID string, base *corev1.PathBase) error {
	head, err := s.getOrInitPathHeadTx(ctx, tx, currentCommitID, base.Path)
	if err != nil {
		return err
	}
	if !head.Exists {
		if base.Exists || base.EntryFingerprint != MissingEntryFingerprint() {
			return ErrConflict
		}
		return nil
	}
	if !base.Exists {
		return ErrConflict
	}
	if head.EntryFingerprint != base.EntryFingerprint {
		return ErrConflict
	}
	return nil
}

func (s *ChangesetStore) getOrInitPathHeadTx(ctx context.Context, tx *sql.Tx, currentCommitID, p string) (*PathHead, error) {
	head, err := getPathHeadTx(ctx, tx, p)
	if err == nil {
		return head, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	entry, err := s.repository.getEntryAtCommitTx(ctx, tx, currentCommitID, p)
	if errors.Is(err, ErrNotFound) {
		if err := insertInitialPathHeadTx(ctx, tx, PathHead{
			Path:             p,
			Exists:           false,
			EntryFingerprint: MissingEntryFingerprint(),
		}, "", ""); err != nil {
			return nil, err
		}
		return getPathHeadTx(ctx, tx, p)
	}
	if err != nil {
		return nil, err
	}
	if err := insertInitialPathHeadTx(ctx, tx, pathHeadFromTreeEntry(*entry), "", ""); err != nil {
		return nil, err
	}
	return getPathHeadTx(ctx, tx, p)
}

func getPathHeadTx(ctx context.Context, tx *sql.Tx, p string) (*PathHead, error) {
	var head PathHead
	var blobID, contentHash sql.NullString
	var mode, size sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		select path, exists, entry_fingerprint, blob_id, content_hash, mode, size
		from path_heads
		where path = $1
		for update
	`, p).Scan(&head.Path, &head.Exists, &head.EntryFingerprint, &blobID, &contentHash, &mode, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if blobID.Valid {
		head.BlobID = blobID.String
	}
	if contentHash.Valid {
		head.ContentHash = contentHash.String
	}
	if mode.Valid {
		head.Mode = uint32(mode.Int64)
	}
	if size.Valid {
		head.Size = size.Int64
	}
	return &head, nil
}

func applyPathHeadEditsTx(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string, edits []*corev1.FileEdit) error {
	for _, edit := range edits {
		switch edit.Op {
		case "mkdir":
			if err := upsertPathHeadTx(ctx, tx, PathHead{
				Path:             edit.Path,
				Exists:           true,
				EntryFingerprint: DirectoryEntryFingerprint(treestore.EmptyRootID()),
			}, changesetID, patchsetID); err != nil {
				return err
			}
		case "delete":
			if err := upsertPathHeadTx(ctx, tx, PathHead{
				Path:             edit.Path,
				Exists:           false,
				EntryFingerprint: MissingEntryFingerprint(),
			}, changesetID, patchsetID); err != nil {
				return err
			}
		case "rename":
			head, err := getPathHeadTx(ctx, tx, edit.OldPath)
			if err != nil {
				return err
			}
			if !head.Exists {
				return ErrConflict
			}
			if err := upsertPathHeadTx(ctx, tx, PathHead{
				Path:             edit.OldPath,
				Exists:           false,
				EntryFingerprint: MissingEntryFingerprint(),
			}, changesetID, patchsetID); err != nil {
				return err
			}
			head.Path = edit.Path
			if err := upsertPathHeadTx(ctx, tx, *head, changesetID, patchsetID); err != nil {
				return err
			}
		default:
			blob, err := getBlobTx(ctx, tx, edit.BlobId)
			if err != nil {
				return err
			}
			entry := FileEntry{
				Path:        edit.Path,
				BlobID:      blob.Id,
				ContentHash: blob.ContentHash,
				Mode:        edit.Mode,
				Size:        blob.Size,
			}
			if err := upsertPathHeadTx(ctx, tx, pathHeadFromFile(entry), changesetID, patchsetID); err != nil {
				return err
			}
		}
	}
	return nil
}

func upsertPathHeadTx(ctx context.Context, tx *sql.Tx, head PathHead, changesetID, patchsetID string) error {
	var blobID, contentHash any
	var mode, size any
	if head.Exists {
		blobID = head.BlobID
		contentHash = head.ContentHash
		mode = int64(head.Mode)
		size = head.Size
	}
	var acceptedChangesetID, acceptedPatchsetID any
	if changesetID != "" {
		acceptedChangesetID = changesetID
	}
	if patchsetID != "" {
		acceptedPatchsetID = patchsetID
	}
	_, err := tx.ExecContext(ctx, `
		insert into path_heads(path, exists, entry_fingerprint, blob_id, content_hash, mode, size, accepted_changeset_id, accepted_patchset_id, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
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
	`, head.Path, head.Exists, head.EntryFingerprint, blobID, contentHash, mode, size, acceptedChangesetID, acceptedPatchsetID)
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
	`, MissingEntryFingerprint(), acceptedChangesetID, acceptedPatchsetID, p, pathHeadLikePrefix(p))
	if err != nil {
		return err
	}
	return upsertPathHeadTx(ctx, tx, PathHead{
		Path:             p,
		Exists:           false,
		EntryFingerprint: MissingEntryFingerprint(),
	}, changesetID, patchsetID)
}

func pathHeadLikePrefix(p string) string {
	prefix := strings.TrimRight(p, "/") + "/"
	prefix = strings.ReplaceAll(prefix, `\`, `\\`)
	prefix = strings.ReplaceAll(prefix, `%`, `\%`)
	prefix = strings.ReplaceAll(prefix, `_`, `\_`)
	return prefix + "%"
}

func insertInitialPathHeadTx(ctx context.Context, tx *sql.Tx, head PathHead, changesetID, patchsetID string) error {
	var blobID, contentHash any
	var mode, size any
	if head.Exists {
		blobID = head.BlobID
		contentHash = head.ContentHash
		mode = int64(head.Mode)
		size = head.Size
	}
	var acceptedChangesetID, acceptedPatchsetID any
	if changesetID != "" {
		acceptedChangesetID = changesetID
	}
	if patchsetID != "" {
		acceptedPatchsetID = patchsetID
	}
	_, err := tx.ExecContext(ctx, `
		insert into path_heads(path, exists, entry_fingerprint, blob_id, content_hash, mode, size, accepted_changeset_id, accepted_patchset_id, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		on conflict (path) do nothing
	`, head.Path, head.Exists, head.EntryFingerprint, blobID, contentHash, mode, size, acceptedChangesetID, acceptedPatchsetID)
	return err
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
