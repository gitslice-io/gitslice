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

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

type ChangesetStore struct {
	db               *sql.DB
	trees            *treestore.Store
	repository       *RepositoryStore
	slices           *SliceStore
	onPendingPublish func()
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

const (
	outboxKindCommitPublished = "commit_published"

	submitRetryAttempts = 5
	submitRetryBaseWait = 5 * time.Millisecond
)

func isRetryableSerializationError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

type commitPublishedPayload struct {
	TargetRef    string    `json:"target_ref"`
	CommitID     string    `json:"commit_id"`
	BaseCommitID string    `json:"base_commit_id"`
	ChangesetID  string    `json:"changeset_id"`
	PatchsetID   string    `json:"patchset_id"`
	ChangedPaths []string  `json:"changed_paths"`
	CommittedAt  time.Time `json:"committed_at"`
}

type publishCommitInsert struct {
	ID              string
	ParentIDsJSON   []byte
	RootTreeID      string
	AuthorSubjectID string
	Message         string
	CreatedAt       time.Time
	ChangedJSON     []byte
}

type publishedPendingUpdate struct {
	PendingID string
	CommitID  string
}

type submittedChangesetUpdate struct {
	ChangesetID string
	CommitID    string
	StackID     string
}

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
	var cs corev1.Changeset
	var account, slice, currentPatchsetID, commitID, pendingPublishID sql.NullString
	var stackID, parentChangesetID, parentPatchsetID sql.NullString
	var stackOrder, stackDepth, siblingOrder sql.NullInt64
	var affectedJSON []byte
	err = s.db.QueryRowContext(ctx, `
			select c.id, c.authoring_account, c.authoring_slice, c.author_subject_id, c.target_ref,
			       c.base_commit_id, c.title, c.description, c.status, c.affected_paths,
			       coalesce(c.current_patchset_number, 0), c.current_patchset_id,
			       c.commit_id, p.id, c.number, c.submit_blocked_reason,
			       c.stack_id, c.stack_order, c.parent_changeset_id, c.parent_patchset_id,
			       case when c.parent_changeset_id is null then 'commit' else 'patchset' end,
			       c.stack_depth, c.sibling_order
			from changesets c
			left join pending_publish p on p.changeset_id = c.id
			where c.id = $1
		`, changesetID).Scan(&cs.Id, &account, &slice, &cs.Author, &cs.TargetRef,
		&cs.BaseCommitId, &cs.Title, &cs.Description, &cs.Status, &affectedJSON,
		&cs.CurrentPatchsetNumber, &currentPatchsetID, &commitID, &pendingPublishID, &cs.Number, &cs.SubmitBlockedReason,
		&stackID, &stackOrder, &parentChangesetID, &parentPatchsetID, &cs.BaseKind, &stackDepth, &siblingOrder)
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
	if stackID.Valid {
		cs.StackId = stackID.String
	}
	if stackOrder.Valid {
		cs.StackOrder = stackOrder.Int64
	}
	if parentChangesetID.Valid {
		cs.ParentChangesetId = parentChangesetID.String
	}
	if parentPatchsetID.Valid {
		cs.ParentPatchsetId = parentPatchsetID.String
	}
	if stackDepth.Valid {
		cs.StackDepth = stackDepth.Int64
	}
	if siblingOrder.Valid {
		cs.SiblingOrder = siblingOrder.Int64
	}
	if err := decodeJSON(affectedJSON, &cs.AffectedPaths); err != nil {
		return nil, err
	}
	patchsets, err := s.listPatchsets(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	cs.Patchsets = patchsets
	storage.PopulateChangesetHandles(&cs)
	if current := currentPatchset(&cs); current != nil {
		cs.SubmitRequirements = current.SubmitRequirements
	} else {
		cs.SubmitRequirements = &corev1.SubmitRequirements{}
	}
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

func (s *ChangesetStore) resolveChangesetSelector(ctx context.Context, selector string) (string, error) {
	prefix, ok := storage.ChangesetIDLookupPrefix(selector)
	if !ok {
		return selector, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		select id
		from changesets
		where left(id, $2) = $1
		order by id
		limit 2
	`, prefix, len(prefix))
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		matches = append(matches, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	switch len(matches) {
	case 0:
		return "", ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("changeset id prefix %q is ambiguous: %w", selector, ErrInvalid)
	}
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

func nullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

func (s *ChangesetStore) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
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
	if patchset.SubmitRequirements == nil || patchset.SubmitRequirements.SourceSliceDefinitionHash == "" {
		req, err := submitRequirementsForSliceTx(ctx, tx, sliceID)
		if err != nil {
			return nil, err
		}
		patchset.SubmitRequirements = req
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
	baseTreeID, err := s.baseTreeForPatchsetTx(ctx, tx, patchset)
	if err != nil {
		return nil, err
	}
	patchset.BaseTreeId = baseTreeID
	resultTreeID := baseTreeID
	if len(patchset.FileEdits) > 0 {
		if s.trees == nil {
			return nil, fmt.Errorf("tree store is not configured")
		}
		treeEdits, err := treeEditsFromPatchsetTx(ctx, tx, patchset.FileEdits)
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
	_, err = tx.ExecContext(ctx, `
		insert into patchsets(
			id, changeset_id, number, base_commit_id, author_subject_id, file_edits,
			changed_paths, coverage, path_bases, read_set, write_set, conflicts, kind, submit_requirements,
			base_kind, base_patchset_id, base_tree_id, result_tree_id, stack_parent_patchset_id, created_at,
			authoring_conversation_id, authoring_conversation_seq
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
	`, patchset.Id, changesetID, patchset.Number, patchset.BaseCommitId, patchset.Author,
		fileEditsJSON, changedJSON, coverageJSON, pathBasesJSON, readSetJSON, writeSetJSON, conflictsJSON, patchset.Kind, submitRequirementsJSON,
		patchset.BaseKind, nullString(patchset.BasePatchsetId), nullString(patchset.BaseTreeId), nullString(patchset.ResultTreeId), nullString(patchset.StackParentPatchsetId), createdAt,
		nullString(patchset.AuthoringConversationId), patchset.AuthoringConversationSeq)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		update changesets
		set current_patchset_id = $1,
		    current_patchset_number = $2,
		    affected_paths = $3,
		    submit_blocked_reason = '',
		    updated_at = now()
		where id = $4
	`, patchset.Id, patchset.Number, changedJSON, changesetID)
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

func (s *ChangesetStore) Approve(ctx context.Context, changesetID, subjectID string) (*corev1.ApproveChangesetResponse, error) {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var patchsetID, status string
	err = tx.QueryRowContext(ctx, `
		select coalesce(current_patchset_id, ''), status
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&patchsetID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if patchsetID == "" {
		return nil, fmt.Errorf("%w: changeset has no current patchset", ErrConflict)
	}
	if status == "abandoned" || status == "submitted" {
		return nil, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `
		insert into approvals(changeset_id, patchset_id, subject_id, created_at)
		values ($1, $2, $3, now())
		on conflict do nothing
	`, changesetID, patchsetID, subjectID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.ApproveChangesetResponse{ChangesetId: changesetID, PatchsetId: patchsetID, SubjectId: subjectID}, nil
}

func (s *ChangesetStore) ReportCheckResult(ctx context.Context, changesetID, subjectID, checkName, resultStatus string) (*corev1.ReportCheckResultResponse, error) {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	checkName = strings.TrimSpace(checkName)
	if subjectID == "" {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	if checkName == "" {
		return nil, fmt.Errorf("%w: check name is required", ErrInvalid)
	}
	resultStatus, ok := storage.NormalizeCheckStatus(resultStatus)
	if !ok {
		return nil, fmt.Errorf("%w: check status must be pass or fail", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var patchsetID, changesetStatus string
	err = tx.QueryRowContext(ctx, `
		select coalesce(current_patchset_id, ''), status
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&patchsetID, &changesetStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if patchsetID == "" {
		return nil, fmt.Errorf("%w: changeset has no current patchset", ErrConflict)
	}
	if changesetStatus == "abandoned" || changesetStatus == "submitted" {
		return nil, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `
		insert into check_results(changeset_id, patchset_id, check_name, status, reported_by, created_at, updated_at)
		values ($1, $2, $3, $4, $5, now(), now())
		on conflict (changeset_id, patchset_id, check_name) do update
		set status = excluded.status,
		    reported_by = excluded.reported_by,
		    updated_at = now()
	`, changesetID, patchsetID, checkName, resultStatus, subjectID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.ReportCheckResultResponse{ChangesetId: changesetID, PatchsetId: patchsetID, CheckName: checkName, Status: resultStatus}, nil
}

func (s *ChangesetStore) Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (res *corev1.SubmitChangesetResponse, err error) {
	return s.SubmitWithCheckStatuses(ctx, changesetID, expectedCurrentPatchsetID, nil)
}

func (s *ChangesetStore) SetPendingPublishListener(fn func()) {
	s.onPendingPublish = fn
}

func (s *ChangesetStore) SubmitWithCheckStatuses(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (res *corev1.SubmitChangesetResponse, err error) {
	defer func() {
		storage.RecordSubmitResult(err)
	}()
retry:
	for attempt := 0; attempt < submitRetryAttempts; attempt++ {
		res, err = s.submitOnce(ctx, changesetID, expectedCurrentPatchsetID, extraCheckStatuses)
		if err == nil || !isRetryableSerializationError(err) || attempt == submitRetryAttempts-1 {
			break
		}
		wait := time.Duration(attempt+1) * submitRetryBaseWait
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			res, err = nil, ctx.Err()
			break retry
		case <-timer.C:
		}
	}
	if err == nil && res != nil && res.Status == "pending_publish" && s.onPendingPublish != nil {
		s.onPendingPublish()
	}
	return res, err
}

func (s *ChangesetStore) submitOnce(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (*corev1.SubmitChangesetResponse, error) {
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
		Account           string
		Slice             string
		SliceID           string
		Number            int64
		CommitID          sql.NullString
		ParentChangesetID sql.NullString
	}
	err = tx.QueryRowContext(ctx, `
			select id, status, coalesce(current_patchset_id, ''), target_ref,
			       base_commit_id, author_subject_id, title, authoring_account, authoring_slice, authoring_slice_id, number, commit_id,
			       parent_changeset_id
			from changesets
			where id = $1
			for update
	`, changesetID).Scan(&cs.ID, &cs.Status, &cs.CurrentPatchsetID, &cs.TargetRef,
		&cs.BaseCommitID, &cs.Author, &cs.Title, &cs.Account, &cs.Slice, &cs.SliceID, &cs.Number, &cs.CommitID,
		&cs.ParentChangesetID)
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
	if len(patchset.Conflicts) > 0 {
		return nil, fmt.Errorf("%w: unresolved patchset conflicts", ErrConflict)
	}
	if cs.ParentChangesetID.Valid {
		var parentStatus string
		err = tx.QueryRowContext(ctx, `
			select status
			from changesets
			where id = $1
			for update
		`, cs.ParentChangesetID.String).Scan(&parentStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if parentStatus != "submitted" && parentStatus != "pending_publish" {
			return nil, blockSubmitTx(ctx, tx, cs.ID, "BlockedOnBaseChangeset")
		}
	}
	latestReq, latestIncludedPaths, err := submitRequirementsAndIncludedPathsForSliceTx(ctx, tx, cs.SliceID)
	if err != nil {
		return nil, err
	}
	for _, p := range patchset.ChangedPaths {
		if !pathInAnyPrefix(latestIncludedPaths, p) {
			return nil, blockSubmitTx(ctx, tx, cs.ID, fmt.Sprintf("changed path %s is outside latest slice definition, refresh the changeset", p))
		}
	}
	approvalSubjects, err := approvalSubjectsTx(ctx, tx, cs.ID, patchset.Id)
	if err != nil {
		return nil, err
	}
	checkStatuses, err := checkStatusesTx(ctx, tx, cs.ID, patchset.Id)
	if err != nil {
		return nil, err
	}
	mergeCheckStatuses(checkStatuses, extraCheckStatuses)
	if reason := storage.EvaluateSubmitRequirements(latestReq, cs.Author, approvalSubjects, checkStatuses); reason != "" {
		return nil, blockSubmitTx(ctx, tx, cs.ID, reason)
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
			if !errors.Is(err, ErrConflict) {
				return nil, err
			}
			return nil, blockSubmitTx(ctx, tx, cs.ID, "path base conflict, refresh or rebase the changeset")
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
		set status = 'pending_publish',
		    submit_blocked_reason = '',
		    updated_at = now()
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

func (s *ChangesetStore) PublishPending(ctx context.Context, limit int) (published int, err error) {
	defer func() {
		storage.RecordPublishBatch(published, err)
	}()
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
		select id, changeset_id, patchset_id, target_ref, created_at
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
		if err := rows.Scan(&row.ID, &row.ChangesetID, &row.PatchsetID, &row.TargetRef, &row.CreatedAt); err != nil {
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
	rootTreeID, err := rootTreeIDForCommitTx(ctx, tx, currentCommitID)
	if err != nil {
		return 0, err
	}
	baseTime := time.Now().UTC().Truncate(time.Microsecond)
	var publishLatencies []time.Duration
	var commitInserts []publishCommitInsert
	var outboxPayloads []commitPublishedPayload
	var pendingUpdates []publishedPendingUpdate
	var changesetUpdates []submittedChangesetUpdate
	updatedBy := "publisher"
	for _, row := range batch {
		var cs struct {
			Author  string
			Title   string
			Status  string
			StackID string
		}
		err := tx.QueryRowContext(ctx, `
				select author_subject_id, title, status, coalesce(stack_id, '')
				from changesets
				where id = $1
				for update
			`, row.ChangesetID).Scan(&cs.Author, &cs.Title, &cs.Status, &cs.StackID)
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
			message = "Submit " + storage.ShortChangesetID(row.ChangesetID)
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
		commitInserts = append(commitInserts, publishCommitInsert{
			ID:              commitID,
			ParentIDsJSON:   parentJSON,
			RootTreeID:      rootTreeID,
			AuthorSubjectID: cs.Author,
			Message:         message,
			CreatedAt:       now,
			ChangedJSON:     changedJSON,
		})
		if err := s.refreshPathHeadsForPatchsetRootTx(ctx, tx, rootTreeID, row.ChangesetID, row.PatchsetID, patchset.FileEdits); err != nil {
			return 0, err
		}
		outboxPayloads = append(outboxPayloads, commitPublishedPayload{
			TargetRef:    targetRef,
			CommitID:     commitID,
			BaseCommitID: baseCommitID,
			ChangesetID:  row.ChangesetID,
			PatchsetID:   row.PatchsetID,
			ChangedPaths: patchset.ChangedPaths,
			CommittedAt:  now,
		})
		pendingUpdates = append(pendingUpdates, publishedPendingUpdate{PendingID: row.ID, CommitID: commitID})
		changesetUpdates = append(changesetUpdates, submittedChangesetUpdate{ChangesetID: row.ChangesetID, CommitID: commitID, StackID: cs.StackID})
		currentCommitID = commitID
		updatedBy = cs.Author
		published++
		publishLatencies = append(publishLatencies, time.Since(row.CreatedAt))
	}
	if published == 0 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if err := insertPublishedCommitsTx(ctx, tx, commitInserts); err != nil {
		return 0, err
	}
	if err := insertCommitPublishedOutboxTx(ctx, tx, outboxPayloads); err != nil {
		return 0, err
	}
	if err := markPendingPublishedTx(ctx, tx, pendingUpdates); err != nil {
		return 0, err
	}
	if err := markChangesetsSubmittedTx(ctx, tx, changesetUpdates); err != nil {
		return 0, err
	}
	if err := refreshClosedStackStatusesForSubmittedTx(ctx, tx, changesetUpdates); err != nil {
		return 0, err
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
		storage.RecordRefCASFailure()
		return 0, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, latency := range publishLatencies {
		storage.ObservePublishLatency(latency)
	}
	return published, nil
}

func (s *ChangesetStore) PendingPublishDepth(ctx context.Context) (int, error) {
	var depth int
	err := s.db.QueryRowContext(ctx, `
		select count(*)
		from pending_publish
		where status = 'pending'
	`).Scan(&depth)
	return depth, err
}

func insertPublishedCommitsTx(ctx context.Context, tx *sql.Tx, rows []publishCommitInsert) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		insert into commits(id, parent_ids, root_tree_id, author_subject_id, message, created_at, changed_paths)
		values `)
	args := make([]any, 0, len(rows)*7)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d)", base, base+1, base+2, base+3, base+4, base+5, base+6))
		args = append(args, row.ID, row.ParentIDsJSON, row.RootTreeID, row.AuthorSubjectID, row.Message, row.CreatedAt, row.ChangedJSON)
	}
	b.WriteString(" on conflict (id) do nothing")
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func insertCommitPublishedOutboxTx(ctx context.Context, tx *sql.Tx, payloads []commitPublishedPayload) error {
	if len(payloads) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		insert into outbox(kind, payload, created_at)
		values `)
	args := make([]any, 0, len(payloads)*3)
	for i, payload := range payloads {
		raw, err := encodeJSON(payload)
		if err != nil {
			return err
		}
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d, $%d)", base, base+1, base+2))
		args = append(args, outboxKindCommitPublished, raw, payload.CommittedAt)
	}
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func markPendingPublishedTx(ctx context.Context, tx *sql.Tx, rows []publishedPendingUpdate) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		update pending_publish as pending
		set status = 'published',
		    commit_id = published.commit_id,
		    updated_at = now(),
		    published_at = now()
		from (values `)
	args := make([]any, 0, len(rows)*2)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d)", base, base+1))
		args = append(args, row.PendingID, row.CommitID)
	}
	b.WriteString(`
		) as published(id, commit_id)
		where pending.id = published.id`)
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func markChangesetsSubmittedTx(ctx context.Context, tx *sql.Tx, rows []submittedChangesetUpdate) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		update changesets as changeset
		set status = 'submitted',
		    commit_id = published.commit_id,
		    updated_at = now()
		from (values `)
	args := make([]any, 0, len(rows)*2)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d)", base, base+1))
		args = append(args, row.ChangesetID, row.CommitID)
	}
	b.WriteString(`
		) as published(id, commit_id)
		where changeset.id = published.id`)
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
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

func insertCommitChangedPathsTx(ctx context.Context, tx *sql.Tx, targetRef, commitID string, changedPaths []string, committedAt time.Time) error {
	if len(changedPaths) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		insert into commit_changed_paths(target_ref, commit_id, path, committed_at)
		values `)
	args := make([]any, 0, len(changedPaths)*4)
	for i, p := range changedPaths {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d)", base, base+1, base+2, base+3))
		args = append(args, targetRef, commitID, p, committedAt)
	}
	b.WriteString(" on conflict do nothing")
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

type pathHeadRefreshTarget struct {
	Path      string
	Recursive bool
}

func (s *ChangesetStore) refreshPathHeadsForPatchsetRootTx(ctx context.Context, tx *sql.Tx, rootTreeID, changesetID, patchsetID string, edits []*corev1.FileEdit) error {
	for _, target := range pathHeadRefreshTargetsForEdits(edits) {
		entry, err := s.repository.getEntryFromTree(ctx, rootTreeID, target.Path)
		if errors.Is(err, ErrNotFound) {
			if err := markPathHeadDeletedRecursiveTx(ctx, tx, target.Path, changesetID, patchsetID); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if target.Recursive && entry.Kind == "directory" {
			if err := s.refreshPathHeadSubtreeTx(ctx, tx, rootTreeID, *entry, changesetID, patchsetID); err != nil {
				return err
			}
			continue
		}
		if err := upsertPathHeadTx(ctx, tx, pathHeadFromTreeEntry(*entry), changesetID, patchsetID); err != nil {
			return err
		}
	}
	return nil
}

func (s *ChangesetStore) refreshPathHeadSubtreeTx(ctx context.Context, tx *sql.Tx, rootTreeID string, entry TreeEntry, changesetID, patchsetID string) error {
	if err := upsertPathHeadTx(ctx, tx, pathHeadFromTreeEntry(entry), changesetID, patchsetID); err != nil {
		return err
	}
	if entry.Kind != "directory" {
		return nil
	}
	children, err := s.repository.listDirectoryFromTree(ctx, rootTreeID, entry.Path)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := s.refreshPathHeadSubtreeTx(ctx, tx, rootTreeID, child, changesetID, patchsetID); err != nil {
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

func submitRequirementsForSliceTx(ctx context.Context, tx *sql.Tx, sliceID string) (*corev1.SubmitRequirements, error) {
	req, _, err := submitRequirementsAndIncludedPathsForSliceTx(ctx, tx, sliceID)
	return req, err
}

func submitRequirementsAndIncludedPathsForSliceTx(ctx context.Context, tx *sql.Tx, sliceID string) (*corev1.SubmitRequirements, []string, error) {
	var requiredApprovals int64
	var includedJSON, requiredChecksJSON []byte
	err := tx.QueryRowContext(ctx, `
		select included_paths, required_approvals, required_checks
		from slices
		where id = $1
	`, sliceID).Scan(&includedJSON, &requiredApprovals, &requiredChecksJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var included []string
	if err := decodeJSON(includedJSON, &included); err != nil {
		return nil, nil, err
	}
	var requiredChecks []string
	if err := decodeJSON(requiredChecksJSON, &requiredChecks); err != nil {
		return nil, nil, err
	}
	return &corev1.SubmitRequirements{
		RequiredApprovals: int32(requiredApprovals),
		RequiredChecks:    requiredChecks,
		SourceSliceDefinitionHash: storage.SubmitRequirementsHash(
			included,
			int32(requiredApprovals),
			requiredChecks,
		),
	}, included, nil
}

func approvalSubjectsTx(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		select subject_id
		from approvals
		where changeset_id = $1 and patchset_id = $2
		order by subject_id
	`, changesetID, patchsetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subjects []string
	for rows.Next() {
		var subjectID string
		if err := rows.Scan(&subjectID); err != nil {
			return nil, err
		}
		subjects = append(subjects, subjectID)
	}
	return subjects, rows.Err()
}

func checkStatusesTx(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `
		select check_name, status
		from check_results
		where changeset_id = $1 and patchset_id = $2
	`, changesetID, patchsetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := map[string]string{}
	for rows.Next() {
		var checkName, status string
		if err := rows.Scan(&checkName, &status); err != nil {
			return nil, err
		}
		statuses[checkName] = status
	}
	return statuses, rows.Err()
}

func mergeCheckStatuses(statuses map[string]string, extra map[string]string) {
	if len(extra) == 0 {
		return
	}
	for checkName, status := range extra {
		checkName = strings.TrimSpace(checkName)
		normalized, ok := storage.NormalizeCheckStatus(status)
		if checkName == "" || !ok {
			continue
		}
		statuses[checkName] = normalized
	}
}

func blockSubmitTx(ctx context.Context, tx *sql.Tx, changesetID, reason string) error {
	_, err := tx.ExecContext(ctx, `
		update changesets
		set submit_blocked_reason = $2,
		    updated_at = now()
		where id = $1
	`, changesetID, reason)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrConflict, reason)
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

func (s *ChangesetStore) listPatchsets(ctx context.Context, changesetID string) ([]*corev1.Patchset, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, changeset_id, number, base_commit_id, author_subject_id, created_at,
		       changed_paths, file_edits, coverage, path_bases, read_set, write_set, conflicts, kind, submit_requirements,
		       base_kind, base_patchset_id, base_tree_id, result_tree_id, stack_parent_patchset_id,
		       authoring_conversation_id, authoring_conversation_seq
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

func (s *ChangesetStore) PatchsetsByConversation(ctx context.Context, conversationID string) ([]*corev1.Patchset, error) {
	if conversationID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, changeset_id, number, base_commit_id, author_subject_id, created_at,
		       changed_paths, file_edits, coverage, path_bases, read_set, write_set, conflicts, kind, submit_requirements,
		       base_kind, base_patchset_id, base_tree_id, result_tree_id, stack_parent_patchset_id,
		       authoring_conversation_id, authoring_conversation_seq
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

func getPatchsetTx(ctx context.Context, tx *sql.Tx, patchsetID string) (*corev1.Patchset, error) {
	row := tx.QueryRowContext(ctx, `
		select id, changeset_id, number, base_commit_id, author_subject_id, created_at,
		       changed_paths, file_edits, coverage, path_bases, read_set, write_set, conflicts, kind, submit_requirements,
		       base_kind, base_patchset_id, base_tree_id, result_tree_id, stack_parent_patchset_id,
		       authoring_conversation_id, authoring_conversation_seq
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
	err := row.Scan(&patchset.Id, &patchset.ChangesetId, &patchset.Number, &patchset.BaseCommitId,
		&patchset.Author, &createdAt, &changedJSON, &fileEditsJSON, &coverageJSON,
		&pathBasesJSON, &readSetJSON, &writeSetJSON, &conflictsJSON, &patchset.Kind, &submitRequirementsJSON,
		&patchset.BaseKind, &basePatchsetID, &baseTreeID, &resultTreeID, &stackParentPatchsetID,
		&authoringConversationID, &patchset.AuthoringConversationSeq)
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
