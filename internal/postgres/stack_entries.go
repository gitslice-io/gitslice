package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func attachStackEntryTx(ctx context.Context, tx *sql.Tx, changesetID, stackID, parentChangesetID, parentPatchsetID string) error {
	stackID = strings.TrimSpace(stackID)
	parentChangesetID = strings.TrimSpace(parentChangesetID)
	parentPatchsetID = strings.TrimSpace(parentPatchsetID)
	if stackID == "" {
		return fmt.Errorf("%w: stack id is required", ErrInvalid)
	}
	var stack struct {
		ID          string
		SliceID     string
		TargetRef   string
		RootEntryID sql.NullString
	}
	err := tx.QueryRowContext(ctx, `
		select id, authoring_slice_id, target_ref, root_entry_changeset_id
		from changeset_stacks
		where id = $1
		for update
	`, stackID).Scan(&stack.ID, &stack.SliceID, &stack.TargetRef, &stack.RootEntryID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var csSliceID, csTargetRef string
	err = tx.QueryRowContext(ctx, `
		select authoring_slice_id, target_ref
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&csSliceID, &csTargetRef)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if csSliceID != stack.SliceID || csTargetRef != stack.TargetRef {
		return fmt.Errorf("%w: changeset stack slice and target ref must match", ErrInvalid)
	}

	depth := int64(0)
	if parentChangesetID == "" {
		if stack.RootEntryID.Valid {
			return fmt.Errorf("%w: stack already has a root entry", ErrConflict)
		}
	} else {
		var parentCurrentPatchsetID sql.NullString
		err = tx.QueryRowContext(ctx, `
			select c.current_patchset_id
			from changesets c
			join changeset_stack_entries e on e.stack_id = $1 and e.changeset_id = c.id
			where c.id = $2
			for update of c
		`, stackID, parentChangesetID).Scan(&parentCurrentPatchsetID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: parent changeset is not in stack", ErrInvalid)
		}
		if err != nil {
			return err
		}
		if parentPatchsetID == "" {
			parentPatchsetID = parentCurrentPatchsetID.String
		}
		if !parentCurrentPatchsetID.Valid || parentPatchsetID == "" || parentPatchsetID != parentCurrentPatchsetID.String {
			return fmt.Errorf("%w: parent patchset is not current", ErrConflict)
		}
		err = tx.QueryRowContext(ctx, `
			select depth + 1
			from changeset_stack_entries
			where stack_id = $1 and changeset_id = $2
		`, stackID, parentChangesetID).Scan(&depth)
		if err != nil {
			return err
		}
	}

	var siblingOrder int64
	if parentChangesetID == "" {
		err = tx.QueryRowContext(ctx, `
			select coalesce(max(sibling_order), 0) + 1
			from changeset_stack_entries
			where stack_id = $1 and parent_changeset_id is null
		`, stackID).Scan(&siblingOrder)
	} else {
		err = tx.QueryRowContext(ctx, `
			select coalesce(max(sibling_order), 0) + 1
			from changeset_stack_entries
			where stack_id = $1 and parent_changeset_id = $2
		`, stackID, parentChangesetID).Scan(&siblingOrder)
	}
	if err != nil {
		return err
	}
	var displayOrder int64
	err = tx.QueryRowContext(ctx, `
		select coalesce(max(display_order), 0) + 1
		from changeset_stack_entries
		where stack_id = $1
	`, stackID).Scan(&displayOrder)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		insert into changeset_stack_entries(
			stack_id, changeset_id, parent_changeset_id, parent_patchset_id,
			sibling_order, display_order, depth, state, created_at, updated_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, 'draft', now(), now())
	`, stackID, changesetID, nullString(parentChangesetID), nullString(parentPatchsetID), siblingOrder, displayOrder, depth)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		update changesets
		set stack_id = $1,
		    stack_order = $2,
		    stack_depth = $3,
		    sibling_order = $4,
		    parent_changeset_id = $5,
		    parent_patchset_id = $6,
		    updated_at = now()
		where id = $7
	`, stackID, displayOrder, depth, siblingOrder, nullString(parentChangesetID), nullString(parentPatchsetID), changesetID)
	if err != nil {
		return err
	}
	rootAssignment := ""
	if parentChangesetID == "" {
		rootAssignment = ", root_entry_changeset_id = $2"
	}
	_, err = tx.ExecContext(ctx, `
		update changeset_stacks
		set active_entry_changeset_id = $2`+rootAssignment+`,
		    updated_at = now()
		where id = $1
	`, stackID, changesetID)
	return err
}

func (s *ChangesetStore) baseTreeForPatchsetTx(ctx context.Context, tx *sql.Tx, patchset *corev1.Patchset) (string, error) {
	if patchset.BaseTreeId != "" {
		return patchset.BaseTreeId, nil
	}
	switch patchset.BaseKind {
	case "", "commit":
		return rootTreeIDForCommitTx(ctx, tx, patchset.BaseCommitId)
	case "patchset":
		basePatchset, err := getPatchsetTx(ctx, tx, patchset.BasePatchsetId)
		if err != nil {
			return "", err
		}
		if basePatchset.ResultTreeId == "" {
			return "", fmt.Errorf("%w: base patchset has no result tree", ErrConflict)
		}
		return basePatchset.ResultTreeId, nil
	default:
		return "", fmt.Errorf("%w: unsupported patchset base kind %q", ErrInvalid, patchset.BaseKind)
	}
}

func updateStackEntryAfterPatchsetTx(ctx context.Context, tx *sql.Tx, stackID, changesetID string, patchset *corev1.Patchset) error {
	parentPatchsetID := ""
	if patchset.BaseKind == "patchset" {
		parentPatchsetID = patchset.BasePatchsetId
	}
	_, err := tx.ExecContext(ctx, `
		update changeset_stack_entries
		set parent_patchset_id = coalesce($3, parent_patchset_id),
		    state = 'draft',
		    updated_at = now()
		where stack_id = $1 and changeset_id = $2
	`, stackID, changesetID, nullString(parentPatchsetID))
	if err != nil {
		return err
	}
	if parentPatchsetID != "" {
		_, err = tx.ExecContext(ctx, `
			update changesets
			set parent_patchset_id = $3,
			    updated_at = now()
			where stack_id = $1 and id = $2
		`, stackID, changesetID, parentPatchsetID)
	}
	return err
}

func markStaleStackChildrenTx(ctx context.Context, tx *sql.Tx, stackID, parentChangesetID, newParentPatchsetID string) error {
	_, err := tx.ExecContext(ctx, `
		update changeset_stack_entries
		set state = 'needs_restack',
		    updated_at = now()
		where stack_id = $1
		  and parent_changeset_id = $2
		  and parent_patchset_id is not null
		  and parent_patchset_id <> $3
	`, stackID, parentChangesetID, newParentPatchsetID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		update changeset_stacks
		set updated_at = now()
		where id = $1
	`, stackID)
	return err
}

func (s *ChangesetStore) listStackEntries(ctx context.Context, stackID string) ([]*corev1.ChangesetStackEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		select changeset_id, parent_changeset_id, parent_patchset_id, sibling_order, display_order, depth, state
		from changeset_stack_entries
		where stack_id = $1
		order by display_order, changeset_id
	`, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*corev1.ChangesetStackEntry
	for rows.Next() {
		entry := &corev1.ChangesetStackEntry{StackId: stackID}
		var parentChangesetID, parentPatchsetID sql.NullString
		if err := rows.Scan(&entry.ChangesetId, &parentChangesetID, &parentPatchsetID, &entry.SiblingOrder, &entry.DisplayOrder, &entry.Depth, &entry.State); err != nil {
			return nil, err
		}
		if parentChangesetID.Valid {
			entry.ParentChangesetId = parentChangesetID.String
		}
		if parentPatchsetID.Valid {
			entry.ParentPatchsetId = parentPatchsetID.String
		}
		cs, err := s.Get(ctx, entry.ChangesetId)
		if err != nil {
			return nil, err
		}
		entry.Changeset = cs
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

type stackEntryMutationRow struct {
	StackID           string
	ChangesetID       string
	ParentChangesetID sql.NullString
	ParentPatchsetID  sql.NullString
	SiblingOrder      int64
	DisplayOrder      int64
	Depth             int64
	State             string
}

func lockStackEntryForMutationTx(ctx context.Context, tx *sql.Tx, stackID, changesetID string) (stackEntryMutationRow, string, error) {
	stackID = strings.TrimSpace(stackID)
	changesetID = strings.TrimSpace(changesetID)
	var entry stackEntryMutationRow
	var status string
	err := tx.QueryRowContext(ctx, `
		select e.stack_id, e.changeset_id, e.parent_changeset_id, e.parent_patchset_id,
		       e.sibling_order, e.display_order, e.depth, e.state, c.status
		from changeset_stack_entries e
		join changesets c on c.id = e.changeset_id
		where e.stack_id = $1 and e.changeset_id = $2
		for update of e, c
	`, stackID, changesetID).Scan(&entry.StackID, &entry.ChangesetID, &entry.ParentChangesetID, &entry.ParentPatchsetID,
		&entry.SiblingOrder, &entry.DisplayOrder, &entry.Depth, &entry.State, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return stackEntryMutationRow{}, "", ErrNotFound
	}
	return entry, status, err
}

func stackEntriesForMutationTx(ctx context.Context, tx *sql.Tx, stackID string) (map[string]stackEntryMutationRow, error) {
	rows, err := tx.QueryContext(ctx, `
		select stack_id, changeset_id, parent_changeset_id, parent_patchset_id,
		       sibling_order, display_order, depth, state
		from changeset_stack_entries
		where stack_id = $1
		for update
	`, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := map[string]stackEntryMutationRow{}
	for rows.Next() {
		var entry stackEntryMutationRow
		if err := rows.Scan(&entry.StackID, &entry.ChangesetID, &entry.ParentChangesetID, &entry.ParentPatchsetID,
			&entry.SiblingOrder, &entry.DisplayOrder, &entry.Depth, &entry.State); err != nil {
			return nil, err
		}
		entries[entry.ChangesetID] = entry
	}
	return entries, rows.Err()
}

func stackEntryHasAncestor(entries map[string]stackEntryMutationRow, changesetID, ancestorID string) bool {
	for changesetID != "" {
		if changesetID == ancestorID {
			return true
		}
		entry, ok := entries[changesetID]
		if !ok || !entry.ParentChangesetID.Valid {
			return false
		}
		changesetID = entry.ParentChangesetID.String
	}
	return false
}

func stackSubtreeChangesetIDs(entries map[string]stackEntryMutationRow, rootChangesetID string) []string {
	descendants := map[string]struct{}{rootChangesetID: {}}
	changed := true
	for changed {
		changed = false
		for _, entry := range entries {
			if _, ok := descendants[entry.ChangesetID]; ok {
				continue
			}
			if entry.ParentChangesetID.Valid {
				if _, ok := descendants[entry.ParentChangesetID.String]; ok {
					descendants[entry.ChangesetID] = struct{}{}
					changed = true
				}
			}
		}
	}
	out := make([]string, 0, len(descendants))
	for id := range descendants {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func stackChangesetIDInList(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func reorderStackSiblingsTx(ctx context.Context, tx *sql.Tx, stackID, parentChangesetID, movedChangesetID string, requestedOrder int64) error {
	entries, err := stackEntriesForMutationTx(ctx, tx, stackID)
	if err != nil {
		return err
	}
	siblings := make([]stackEntryMutationRow, 0)
	for _, entry := range entries {
		parentID := ""
		if entry.ParentChangesetID.Valid {
			parentID = entry.ParentChangesetID.String
		}
		if parentID == parentChangesetID && entry.ChangesetID != movedChangesetID {
			siblings = append(siblings, entry)
		}
	}
	moved, ok := entries[movedChangesetID]
	if !ok {
		return ErrNotFound
	}
	sort.Slice(siblings, func(i, j int) bool {
		if siblings[i].SiblingOrder == siblings[j].SiblingOrder {
			return siblings[i].ChangesetID < siblings[j].ChangesetID
		}
		return siblings[i].SiblingOrder < siblings[j].SiblingOrder
	})
	if requestedOrder <= 0 {
		requestedOrder = int64(len(siblings) + 1)
	}
	index := int(requestedOrder - 1)
	if index < 0 {
		index = 0
	}
	if index > len(siblings) {
		index = len(siblings)
	}
	siblings = append(siblings, stackEntryMutationRow{})
	copy(siblings[index+1:], siblings[index:])
	siblings[index] = moved
	for i, entry := range siblings {
		tempOrder := int64(-1000000 - i)
		if _, err := tx.ExecContext(ctx, `
			update changeset_stack_entries
			set sibling_order = $3
			where stack_id = $1 and changeset_id = $2
		`, stackID, entry.ChangesetID, tempOrder); err != nil {
			return err
		}
	}
	for i, entry := range siblings {
		order := int64(i + 1)
		if _, err := tx.ExecContext(ctx, `
			update changeset_stack_entries
			set sibling_order = $3,
			    updated_at = now()
			where stack_id = $1 and changeset_id = $2
		`, stackID, entry.ChangesetID, order); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			update changesets
			set sibling_order = $3,
			    updated_at = now()
			where stack_id = $1 and id = $2
		`, stackID, entry.ChangesetID, order); err != nil {
			return err
		}
	}
	return nil
}

func markSubtreeNeedsRestackTx(ctx context.Context, tx *sql.Tx, stackID, rootChangesetID string) error {
	entries, err := stackEntriesForMutationTx(ctx, tx, stackID)
	if err != nil {
		return err
	}
	descendants := map[string]struct{}{rootChangesetID: {}}
	changed := true
	for changed {
		changed = false
		for _, entry := range entries {
			if _, ok := descendants[entry.ChangesetID]; ok {
				continue
			}
			if entry.ParentChangesetID.Valid {
				if _, ok := descendants[entry.ParentChangesetID.String]; ok {
					descendants[entry.ChangesetID] = struct{}{}
					changed = true
				}
			}
		}
	}
	for changesetID := range descendants {
		if _, err := tx.ExecContext(ctx, `
			update changeset_stack_entries
			set state = 'needs_restack',
			    updated_at = now()
			where stack_id = $1 and changeset_id = $2
		`, stackID, changesetID); err != nil {
			return err
		}
	}
	return nil
}

func recomputeStackDisplayTx(ctx context.Context, tx *sql.Tx, stackID string) error {
	entries, err := stackEntriesForMutationTx(ctx, tx, stackID)
	if err != nil {
		return err
	}
	children := map[string][]stackEntryMutationRow{}
	var roots []stackEntryMutationRow
	for _, entry := range entries {
		if entry.ParentChangesetID.Valid {
			children[entry.ParentChangesetID.String] = append(children[entry.ParentChangesetID.String], entry)
		} else {
			roots = append(roots, entry)
		}
	}
	sortStackMutationRows(roots)
	for parent := range children {
		sortStackMutationRows(children[parent])
	}
	type displayUpdate struct {
		ChangesetID       string
		ParentChangesetID sql.NullString
		ParentPatchsetID  sql.NullString
		DisplayOrder      int64
		Depth             int64
	}
	var updates []displayUpdate
	var order int64
	var walk func([]stackEntryMutationRow, int64)
	walk = func(rows []stackEntryMutationRow, depth int64) {
		for _, entry := range rows {
			order++
			updates = append(updates, displayUpdate{
				ChangesetID:       entry.ChangesetID,
				ParentChangesetID: entry.ParentChangesetID,
				ParentPatchsetID:  entry.ParentPatchsetID,
				DisplayOrder:      order,
				Depth:             depth,
			})
			walk(children[entry.ChangesetID], depth+1)
		}
	}
	walk(roots, 0)
	if len(updates) != len(entries) {
		return ErrConflict
	}
	for i, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			update changeset_stack_entries
			set display_order = $3
			where stack_id = $1 and changeset_id = $2
		`, stackID, update.ChangesetID, int64(-1000000-i)); err != nil {
			return err
		}
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			update changeset_stack_entries
			set display_order = $3,
			    depth = $4,
			    updated_at = now()
			where stack_id = $1 and changeset_id = $2
		`, stackID, update.ChangesetID, update.DisplayOrder, update.Depth); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			update changesets
			set stack_order = $3,
			    stack_depth = $4,
			    parent_changeset_id = $5,
			    parent_patchset_id = $6,
			    updated_at = now()
			where stack_id = $1 and id = $2
		`, stackID, update.ChangesetID, update.DisplayOrder, update.Depth, update.ParentChangesetID, update.ParentPatchsetID); err != nil {
			return err
		}
	}
	return nil
}

func sortStackMutationRows(rows []stackEntryMutationRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SiblingOrder == rows[j].SiblingOrder {
			return rows[i].ChangesetID < rows[j].ChangesetID
		}
		return rows[i].SiblingOrder < rows[j].SiblingOrder
	})
}

func refreshClosedStackStatusesForSubmittedTx(ctx context.Context, tx *sql.Tx, rows []submittedChangesetUpdate) error {
	seen := map[string]struct{}{}
	for _, row := range rows {
		if row.StackID == "" {
			continue
		}
		if _, ok := seen[row.StackID]; ok {
			continue
		}
		seen[row.StackID] = struct{}{}
		if err := refreshClosedStackStatusTx(ctx, tx, row.StackID); err != nil {
			return err
		}
	}
	return nil
}

func refreshClosedStackStatusTx(ctx context.Context, tx *sql.Tx, stackID string) error {
	if strings.TrimSpace(stackID) == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		update changeset_stacks as stack
		set status = 'closed',
		    updated_at = now()
		where stack.id = $1
		  and exists (
		    select 1
		    from changeset_stack_entries entry
		    where entry.stack_id = stack.id
		  )
		  and not exists (
		    select 1
		    from changeset_stack_entries entry
		    join changesets changeset on changeset.id = entry.changeset_id
		    where entry.stack_id = stack.id
		      and changeset.status not in ('submitted', 'abandoned')
		  )
	`, stackID)
	return err
}
