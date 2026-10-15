package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) CreateStack(ctx context.Context, subjectID string, req *corev1.CreateStackRequest) (*corev1.ChangesetStack, error) {
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
	id, err := objectid.RandomID("stk")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		insert into changeset_stacks(
			id, authoring_account, authoring_slice, authoring_slice_id, target_ref,
			base_commit_id, title, status, created_by, created_at, updated_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, 'open', $8, $9, $9)
	`, id, req.AuthoringSlice.Account, req.AuthoringSlice.Slice, slice.Id, targetRef, baseCommitID, req.Title, subjectID, now)
	if err != nil {
		return nil, err
	}
	return s.GetStack(ctx, id)
}

func (s *ChangesetStore) GetStack(ctx context.Context, stackID string) (*corev1.ChangesetStack, error) {
	var stack corev1.ChangesetStack
	var account, slice, activeEntryID, rootEntryID sql.NullString
	var createdAt, updatedAt time.Time
	err := s.db.QueryRowContext(ctx, `
		select id, authoring_account, authoring_slice, target_ref, base_commit_id,
		       title, status, active_entry_changeset_id, root_entry_changeset_id,
		       created_by, created_at, updated_at
		from changeset_stacks
		where id = $1
	`, stackID).Scan(&stack.Id, &account, &slice, &stack.TargetRef, &stack.BaseCommitId,
		&stack.Title, &stack.Status, &activeEntryID, &rootEntryID, &stack.CreatedBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	stack.AuthoringSlice = &corev1.SliceRef{Account: account.String, Slice: slice.String}
	if activeEntryID.Valid {
		stack.ActiveEntryId = activeEntryID.String
	}
	if rootEntryID.Valid {
		stack.RootEntryId = rootEntryID.String
	}
	stack.CreatedAt = formatTime(createdAt)
	stack.UpdatedAt = formatTime(updatedAt)
	entries, err := s.listStackEntries(ctx, stack.Id)
	if err != nil {
		return nil, err
	}
	stack.Entries = entries
	return &stack, nil
}

func (s *ChangesetStore) ListStacks(ctx context.Context, req *corev1.ListStacksRequest) ([]*corev1.ChangesetStack, error) {
	limit := int(req.Limit)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	status := strings.TrimSpace(req.Status)
	switch {
	case req.AuthoringSlice != nil && status != "":
		rows, err = s.db.QueryContext(ctx, `
			select id
			from changeset_stacks
			where authoring_account = $1 and authoring_slice = $2 and status = $3
			order by updated_at desc, id desc
			limit $4
		`, req.AuthoringSlice.Account, req.AuthoringSlice.Slice, status, limit)
	case req.AuthoringSlice != nil:
		rows, err = s.db.QueryContext(ctx, `
			select id
			from changeset_stacks
			where authoring_account = $1 and authoring_slice = $2 and status <> 'closed'
			order by updated_at desc, id desc
			limit $3
		`, req.AuthoringSlice.Account, req.AuthoringSlice.Slice, limit)
	case status != "":
		rows, err = s.db.QueryContext(ctx, `
			select id
			from changeset_stacks
			where status = $1
			order by updated_at desc, id desc
			limit $2
		`, status, limit)
	default:
		rows, err = s.db.QueryContext(ctx, `
			select id
			from changeset_stacks
			where status <> 'closed'
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
	out := make([]*corev1.ChangesetStack, 0, len(ids))
	for _, id := range ids {
		stack, err := s.GetStack(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, stack)
	}
	return out, nil
}

func (s *ChangesetStore) SetStackBaseCommit(ctx context.Context, stackID, baseCommitID string) error {
	stackID = strings.TrimSpace(stackID)
	baseCommitID = strings.TrimSpace(baseCommitID)
	if stackID == "" || baseCommitID == "" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `
		update changeset_stacks
		set base_commit_id = $2,
		    updated_at = now()
		where id = $1
	`, stackID, baseCommitID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *ChangesetStore) SetStackStatus(ctx context.Context, stackID, stackStatus string) error {
	stackID = strings.TrimSpace(stackID)
	stackStatus = strings.TrimSpace(stackStatus)
	if stackID == "" || stackStatus == "" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `
		update changeset_stacks
		set status = $2,
		    updated_at = now()
		where id = $1
	`, stackID, stackStatus)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *ChangesetStore) MoveStackEntry(ctx context.Context, req *corev1.MoveStackEntryRequest) (*corev1.ChangesetStack, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	entry, status, err := lockStackEntryForMutationTx(ctx, tx, req.StackId, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	if status == "submitted" {
		return nil, ErrConflict
	}
	if err := reorderStackSiblingsTx(ctx, tx, req.StackId, entry.ParentChangesetID.String, entry.ChangesetID, req.SiblingOrder); err != nil {
		return nil, err
	}
	if err := recomputeStackDisplayTx(ctx, tx, req.StackId); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update changeset_stacks set updated_at = now() where id = $1`, req.StackId); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetStack(ctx, req.StackId)
}

func (s *ChangesetStore) ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	entry, status, err := lockStackEntryForMutationTx(ctx, tx, req.StackId, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	if status == "submitted" {
		return nil, ErrConflict
	}
	entries, err := stackEntriesForMutationTx(ctx, tx, req.StackId)
	if err != nil {
		return nil, err
	}
	newParentID := strings.TrimSpace(req.NewParentChangesetId)
	newParentPatchsetID := strings.TrimSpace(req.NewParentPatchsetId)
	if newParentID == "" {
		var rootID sql.NullString
		err := tx.QueryRowContext(ctx, `
			select root_entry_changeset_id
			from changeset_stacks
			where id = $1
			for update
		`, req.StackId).Scan(&rootID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if rootID.Valid && rootID.String != entry.ChangesetID {
			return nil, ErrConflict
		}
		newParentPatchsetID = ""
	} else {
		parent, ok := entries[newParentID]
		if !ok || parent.ChangesetID == "" {
			return nil, ErrInvalid
		}
		if stackEntryHasAncestor(entries, newParentID, entry.ChangesetID) {
			return nil, ErrConflict
		}
		var parentCurrentPatchsetID sql.NullString
		err := tx.QueryRowContext(ctx, `
			select current_patchset_id
			from changesets
			where id = $1
			for update
		`, newParentID).Scan(&parentCurrentPatchsetID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if newParentPatchsetID == "" {
			newParentPatchsetID = parentCurrentPatchsetID.String
		}
		if !parentCurrentPatchsetID.Valid || newParentPatchsetID == "" || newParentPatchsetID != parentCurrentPatchsetID.String {
			return nil, ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, `
		update changeset_stack_entries
		set parent_changeset_id = $3,
		    parent_patchset_id = $4,
		    sibling_order = -1,
		    state = 'needs_restack',
		    updated_at = now()
		where stack_id = $1 and changeset_id = $2
	`, req.StackId, entry.ChangesetID, nullString(newParentID), nullString(newParentPatchsetID))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		update changesets
		set parent_changeset_id = $3,
		    parent_patchset_id = $4,
		    sibling_order = -1,
		    updated_at = now()
		where stack_id = $1 and id = $2
	`, req.StackId, entry.ChangesetID, nullString(newParentID), nullString(newParentPatchsetID))
	if err != nil {
		return nil, err
	}
	if newParentID == "" {
		if _, err := tx.ExecContext(ctx, `
			update changeset_stacks
			set root_entry_changeset_id = $2
			where id = $1
		`, req.StackId, entry.ChangesetID); err != nil {
			return nil, err
		}
	}
	if err := markSubtreeNeedsRestackTx(ctx, tx, req.StackId, entry.ChangesetID); err != nil {
		return nil, err
	}
	if err := reorderStackSiblingsTx(ctx, tx, req.StackId, newParentID, entry.ChangesetID, req.SiblingOrder); err != nil {
		return nil, err
	}
	if err := recomputeStackDisplayTx(ctx, tx, req.StackId); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		update changeset_stacks
		set active_entry_changeset_id = $2,
		    updated_at = now()
		where id = $1
	`, req.StackId, entry.ChangesetID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetStack(ctx, req.StackId)
}

func (s *ChangesetStore) DetachStackEntry(ctx context.Context, subjectID string, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sourceID := strings.TrimSpace(req.StackId)
	changesetID := strings.TrimSpace(req.ChangesetId)
	var source struct {
		ID          string
		Account     string
		Slice       string
		SliceID     string
		TargetRef   string
		BaseCommit  string
		Title       string
		CreatedBy   string
		ActiveEntry sql.NullString
		RootEntry   sql.NullString
	}
	err = tx.QueryRowContext(ctx, `
		select id, authoring_account, authoring_slice, authoring_slice_id,
		       target_ref, base_commit_id, title, created_by,
		       active_entry_changeset_id, root_entry_changeset_id
		from changeset_stacks
		where id = $1
		for update
	`, sourceID).Scan(&source.ID, &source.Account, &source.Slice, &source.SliceID,
		&source.TargetRef, &source.BaseCommit, &source.Title, &source.CreatedBy,
		&source.ActiveEntry, &source.RootEntry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	entries, err := stackEntriesForMutationTx(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	entry, ok := entries[changesetID]
	if !ok {
		return nil, ErrNotFound
	}
	if !entry.ParentChangesetID.Valid {
		return nil, ErrInvalid
	}
	descendants := stackSubtreeChangesetIDs(entries, changesetID)
	selectedTitle := ""
	for _, id := range descendants {
		var status, title string
		err := tx.QueryRowContext(ctx, `
			select status, title
			from changesets
			where id = $1
			for update
		`, id).Scan(&status, &title)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if status == "submitted" {
			return nil, ErrConflict
		}
		if id == changesetID {
			selectedTitle = title
		}
	}
	detachedID, err := objectid.RandomID("stk")
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(selectedTitle)
	}
	if title == "" {
		title = strings.TrimSpace(source.Title)
	}
	if title == "" {
		title = "Detached stack"
	}
	createdBy := strings.TrimSpace(subjectID)
	if createdBy == "" {
		createdBy = source.CreatedBy
	}
	_, err = tx.ExecContext(ctx, `
		insert into changeset_stacks(
			id, authoring_account, authoring_slice, authoring_slice_id,
			target_ref, base_commit_id, title, status,
			active_entry_changeset_id, root_entry_changeset_id,
			created_by, created_at, updated_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, 'open', $8, $8, $9, now(), now())
	`, detachedID, source.Account, source.Slice, source.SliceID, source.TargetRef,
		source.BaseCommit, title, changesetID, createdBy)
	if err != nil {
		return nil, err
	}
	for _, id := range descendants {
		if _, err := tx.ExecContext(ctx, `
			update changeset_stack_entries
			set stack_id = $2,
			    state = 'needs_restack',
			    updated_at = now()
			where stack_id = $1 and changeset_id = $3
		`, sourceID, detachedID, id); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			update changesets
			set stack_id = $2,
			    updated_at = now()
			where stack_id = $1 and id = $3
		`, sourceID, detachedID, id); err != nil {
			return nil, err
		}
	}
	_, err = tx.ExecContext(ctx, `
		update changeset_stack_entries
		set parent_changeset_id = null,
		    parent_patchset_id = null,
		    sibling_order = 1,
		    updated_at = now()
		where stack_id = $1 and changeset_id = $2
	`, detachedID, changesetID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		update changesets
		set parent_changeset_id = null,
		    parent_patchset_id = null,
		    base_kind = 'commit',
		    sibling_order = 1,
		    updated_at = now()
		where stack_id = $1 and id = $2
	`, detachedID, changesetID)
	if err != nil {
		return nil, err
	}
	activeEntryID := source.ActiveEntry
	if activeEntryID.Valid {
		if stackChangesetIDInList(descendants, activeEntryID.String) {
			activeEntryID = source.RootEntry
		}
	}
	_, err = tx.ExecContext(ctx, `
		update changeset_stacks
		set active_entry_changeset_id = $2,
		    updated_at = now()
		where id = $1
	`, sourceID, activeEntryID)
	if err != nil {
		return nil, err
	}
	if err := recomputeStackDisplayTx(ctx, tx, sourceID); err != nil {
		return nil, err
	}
	if err := recomputeStackDisplayTx(ctx, tx, detachedID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sourceStack, err := s.GetStack(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	detachedStack, err := s.GetStack(ctx, detachedID)
	if err != nil {
		return nil, err
	}
	return &corev1.DetachStackEntryResponse{SourceStack: sourceStack, DetachedStack: detachedStack}, nil
}
