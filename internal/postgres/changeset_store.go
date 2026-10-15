package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type ChangesetStore struct {
	db               *sql.DB
	trees            *treestore.Store
	repository       *RepositoryStore
	slices           *SliceStore
	onPendingPublish func()
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockSliceForChangesetNumber(ctx, tx, slice.Id); err != nil {
		return nil, err
	}
	number, err := nextChangesetNumberTx(ctx, tx, slice.Id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
			insert into changesets(
				id, number, authoring_account, authoring_slice, authoring_slice_id, author_subject_id,
				target_ref, base_commit_id, title, description, status, affected_paths,
				current_patchset_number, created_at, updated_at
			)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'draft', $11, 0, $12, $12)
		`, id, number, req.AuthoringSlice.Account, req.AuthoringSlice.Slice, slice.Id, subjectID,
		targetRef, baseCommitID, req.Title, req.Description, empty, now)
	if err != nil {
		return nil, err
	}
	if req.StackId != "" {
		if err := attachStackEntryTx(ctx, tx, id, req.StackId, req.ParentChangesetId, req.ParentPatchsetId); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *ChangesetStore) Get(ctx context.Context, changesetID string) (*corev1.Changeset, error) {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	cs, _, err := scanChangesetRow(s.db.QueryRowContext(ctx, `
			select `+changesetRowColumns+`, null::jsonb
			from changesets c
			left join pending_publish p on p.changeset_id = c.id
			where c.id = $1
		`, changesetID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	patchsets, err := s.listPatchsets(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	cs.Patchsets = patchsets
	storage.PopulateChangesetHandles(cs)
	if current := currentPatchset(cs); current != nil {
		cs.SubmitRequirements = current.SubmitRequirements
	} else {
		cs.SubmitRequirements = &corev1.SubmitRequirements{}
	}
	return cs, nil
}

// changesetRowColumns are a changeset's own fields, read with the
// pending_publish row joined as p. scanChangesetRow reads them, plus one more
// column: the current patchset's submit requirements (or null).
const changesetRowColumns = `c.id, c.authoring_account, c.authoring_slice, c.author_subject_id, c.target_ref,
			       c.base_commit_id, c.title, c.description, c.status, c.affected_paths,
			       coalesce(c.current_patchset_number, 0), c.current_patchset_id,
			       c.commit_id, p.id, c.number, c.submit_blocked_reason,
			       c.stack_id, c.stack_order, c.parent_changeset_id, c.parent_patchset_id,
			       case when c.parent_changeset_id is null then 'commit' else 'patchset' end,
			       c.stack_depth, c.sibling_order`

func scanChangesetRow(row rowScanner) (*corev1.Changeset, []byte, error) {
	var cs corev1.Changeset
	var account, slice, currentPatchsetID, commitID, pendingPublishID sql.NullString
	var stackID, parentChangesetID, parentPatchsetID sql.NullString
	var stackOrder, stackDepth, siblingOrder sql.NullInt64
	var affectedJSON, submitRequirementsJSON []byte
	if err := row.Scan(&cs.Id, &account, &slice, &cs.Author, &cs.TargetRef,
		&cs.BaseCommitId, &cs.Title, &cs.Description, &cs.Status, &affectedJSON,
		&cs.CurrentPatchsetNumber, &currentPatchsetID, &commitID, &pendingPublishID, &cs.Number, &cs.SubmitBlockedReason,
		&stackID, &stackOrder, &parentChangesetID, &parentPatchsetID, &cs.BaseKind, &stackDepth, &siblingOrder,
		&submitRequirementsJSON); err != nil {
		return nil, nil, err
	}
	if account.Valid || slice.Valid {
		cs.AuthoringSlice = &corev1.SliceRef{Account: account.String, Slice: slice.String}
	}
	cs.CurrentPatchsetId = currentPatchsetID.String
	cs.CommitId = commitID.String
	cs.PendingPublishId = pendingPublishID.String
	cs.StackId = stackID.String
	cs.StackOrder = stackOrder.Int64
	cs.ParentChangesetId = parentChangesetID.String
	cs.ParentPatchsetId = parentPatchsetID.String
	cs.StackDepth = stackDepth.Int64
	cs.SiblingOrder = siblingOrder.Int64
	if err := decodeJSON(affectedJSON, &cs.AffectedPaths); err != nil {
		return nil, nil, err
	}
	return &cs, submitRequirementsJSON, nil
}

// listSummaries reads changesets without their patchsets, in one query, in the
// order of ids. Submit requirements come from each current patchset.
func (s *ChangesetStore) listSummaries(ctx context.Context, ids []string) ([]*corev1.Changeset, error) {
	rows, err := s.db.QueryContext(ctx, `
		select `+changesetRowColumns+`, cp.submit_requirements
		from changesets c
		left join pending_publish p on p.changeset_id = c.id
		left join patchsets cp on cp.id = c.current_patchset_id
		where c.id = any($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*corev1.Changeset{}
	for rows.Next() {
		cs, requirementsJSON, err := scanChangesetRow(rows)
		if err != nil {
			return nil, err
		}
		cs.SubmitRequirements = &corev1.SubmitRequirements{}
		if len(requirementsJSON) > 0 {
			if err := decodeJSON(requirementsJSON, &cs.SubmitRequirements); err != nil {
				return nil, err
			}
		}
		storage.PopulateChangesetHandles(cs)
		byID[cs.Id] = cs
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*corev1.Changeset, 0, len(ids))
	for _, id := range ids {
		if cs := byID[id]; cs != nil {
			out = append(out, cs)
		}
	}
	return out, nil
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
	if req.Summary {
		return s.listSummaries(ctx, ids)
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

func (s *ChangesetStore) resolveChangesetSelector(ctx context.Context, selector string) (string, error) {
	prefix, isChangeset := storage.ChangesetIDLookupPrefix(selector)
	commitPrefix, isCommit := storage.CommitIDLookupPrefix(selector)
	if !isChangeset && !isCommit {
		return selector, nil
	}
	if isChangeset {
		matches, err := s.selectChangesetIDs(ctx, `select id from changesets where left(id, $2) = $1 order by id limit 2`, prefix, len(prefix))
		if err != nil {
			return "", err
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("changeset id prefix %q is ambiguous: %w", selector, ErrInvalid)
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	if isCommit {
		// Not a changeset: maybe the id of the commit a changeset landed as.
		matches, err := s.selectChangesetIDs(ctx, `select id from changesets where commit_id like $1 || '%' order by id limit 2`, commitPrefix)
		if err != nil {
			return "", err
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("commit id prefix %q is ambiguous: %w", selector, ErrInvalid)
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	if !isChangeset {
		return selector, nil
	}
	return "", ErrNotFound
}

func (s *ChangesetStore) selectChangesetIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		matches = append(matches, id)
	}
	return matches, rows.Err()
}

func lockSliceForChangesetNumber(ctx context.Context, tx *sql.Tx, sliceID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `
		select id
		from slices
		where id = $1
		for update
	`, sliceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func nextChangesetNumberTx(ctx context.Context, tx *sql.Tx, sliceID string) (int64, error) {
	var number int64
	err := tx.QueryRowContext(ctx, `
		select coalesce(max(number), 0) + 1
		from changesets
		where authoring_slice_id = $1
	`, sliceID).Scan(&number)
	return number, err
}

func nullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

func (s *ChangesetStore) Abandon(ctx context.Context, changesetID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var activeChildren int
	err = tx.QueryRowContext(ctx, `
		select count(*)
		from changeset_stack_entries e
		join changesets child on child.id = e.changeset_id
		where e.parent_changeset_id = $1
		  and child.status not in ('submitted', 'abandoned')
	`, changesetID).Scan(&activeChildren)
	if err != nil {
		return err
	}
	if activeChildren > 0 {
		return ErrConflict
	}
	var stackID string
	err = tx.QueryRowContext(ctx, `
		update changesets
		set status = 'abandoned', updated_at = now()
		where id = $1 and status <> 'submitted'
		returning coalesce(stack_id, '')
	`, changesetID).Scan(&stackID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if err := refreshClosedStackStatusTx(ctx, tx, stackID); err != nil {
		return err
	}
	return tx.Commit()
}

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
	if cs == nil || len(cs.Patchsets) == 0 {
		return nil
	}
	for _, patchset := range cs.Patchsets {
		if patchset.Id == cs.CurrentPatchsetId {
			return patchset
		}
	}
	return cs.Patchsets[len(cs.Patchsets)-1]
}

func pathInAnyPrefix(prefixes []string, p string) bool {
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(prefix, "/")
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}
