package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

var (
	ErrConflict        = errors.New("conflict")
	ErrNotFound        = errors.New("not found")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrUnauthorized    = errors.New("unauthorized")
)

const DefaultTargetRef = "refs/global/main"

type Store struct {
	db    *sql.DB
	trees *treestore.Store
}

type Subject struct {
	ID          string
	DisplayName string
}

type FileEntry struct {
	Path        string
	BlobID      string
	ContentHash string
	Mode        uint32
	Size        int64
}

type PathHead struct {
	Path             string
	Exists           bool
	EntryFingerprint string
	BlobID           string
	ContentHash      string
	Mode             uint32
	Size             int64
}

type pendingPublishRow struct {
	ID          string
	ChangesetID string
	PatchsetID  string
	TargetRef   string
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) SetTreeStore(trees *treestore.Store) {
	s.trees = trees
}

func (s *Store) Migrate(ctx context.Context) error {
	for _, stmt := range migrationStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if s.trees != nil {
		if err := s.trees.EnsureEmptyRoot(ctx); err != nil {
			return err
		}
	}
	return s.seedDevFixture(ctx)
}

func (s *Store) LoginDevUser(ctx context.Context, devUser string) (string, string, error) {
	subjectID := normalizeDevSubject(devUser)
	var found string
	err := s.db.QueryRowContext(ctx, `select id from subjects where id = $1`, subjectID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	token, err := objectid.RandomID("devtok")
	if err != nil {
		return "", "", err
	}
	sessionID, err := objectid.RandomID("sess")
	if err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx, `
		insert into sessions(id, subject_id, token_hash, expires_at)
		values ($1, $2, $3, $4)
	`, sessionID, subjectID, tokenHash(token), time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		return "", "", err
	}
	return token, subjectID, nil
}

func (s *Store) SubjectForToken(ctx context.Context, token string) (*Subject, error) {
	var subject Subject
	err := s.db.QueryRowContext(ctx, `
		select subjects.id, subjects.display_name
		from sessions
		join subjects on subjects.id = sessions.subject_id
		where sessions.token_hash = $1
		  and sessions.revoked_at is null
		  and sessions.expires_at > now()
	`, tokenHash(token)).Scan(&subject.ID, &subject.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	return &subject, nil
}

func (s *Store) EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error {
	var ok bool
	err := s.db.QueryRowContext(ctx, `
		select exists(
			select 1
			from account_memberships m
			join accounts a on a.id = m.account_id
			where a.slug = $1 and m.subject_id = $2
		)
	`, accountSlug, subjectID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUnauthorized
	}
	return nil
}

func (s *Store) ResolveSlice(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	if ref == nil || ref.Account == "" || ref.Slice == "" {
		return nil, fmt.Errorf("slice ref requires account and slice")
	}
	row := s.db.QueryRowContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       slices.visibility, slices.included_paths
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1 and slices.slug = $2
	`, ref.Account, ref.Slice)
	return scanSlice(row)
}

func (s *Store) GetSlice(ctx context.Context, sliceID string) (*corev1.Slice, error) {
	row := s.db.QueryRowContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       slices.visibility, slices.included_paths
		from slices
		join accounts on accounts.id = slices.account_id
		where slices.id = $1
	`, sliceID)
	return scanSlice(row)
}

func (s *Store) ListSlices(ctx context.Context, account string, limit int) ([]*corev1.Slice, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       slices.visibility, slices.included_paths
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1
		order by slices.slug
		limit $2
	`, account, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.Slice
	for rows.Next() {
		slice, err := scanSlice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, slice)
	}
	return out, rows.Err()
}

func (s *Store) UpdateSliceDefinition(ctx context.Context, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	if definition == nil {
		return nil, fmt.Errorf("slice definition is required")
	}
	included, err := encodeJSON(definition.IncludedPaths)
	if err != nil {
		return nil, err
	}
	nextHash := definitionHash(sliceID, definition.Version+1, definition.IncludedPaths, definition.Visibility)
	res, err := s.db.ExecContext(ctx, `
		update slices
		set version = version + 1,
		    definition_hash = $1,
		    visibility = $2,
		    included_paths = $3,
		    updated_at = now()
		where id = $4 and definition_hash = $5
	`, nextHash, definition.Visibility, included, sliceID, expectedHash)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrConflict
	}
	slice, err := s.GetSlice(ctx, sliceID)
	if err != nil {
		return nil, err
	}
	return slice.Definition, nil
}

func (s *Store) GetRef(ctx context.Context, name string) (*corev1.Ref, error) {
	var ref corev1.Ref
	var updatedAt time.Time
	err := s.db.QueryRowContext(ctx, `
		select name, commit_id, updated_at, coalesce(updated_by, '')
		from refs
		where name = $1
	`, name).Scan(&ref.Name, &ref.CommitId, &updatedAt, &ref.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ref.UpdatedAt = formatTime(updatedAt)
	return &ref, nil
}

func (s *Store) GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error) {
	var commit corev1.Commit
	var parentJSON, changedJSON []byte
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
		select id, parent_ids, root_tree_id, coalesce(author_subject_id, ''),
		       message, created_at, changed_paths
		from commits
		where id = $1
	`, commitID).Scan(&commit.Id, &parentJSON, &commit.RootTreeId, &commit.Author, &commit.Message, &createdAt, &changedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	commit.CreatedAt = formatTime(createdAt)
	if err := decodeJSON(parentJSON, &commit.ParentIds); err != nil {
		return nil, err
	}
	if err := decodeJSON(changedJSON, &commit.ChangedPaths); err != nil {
		return nil, err
	}
	return &commit, nil
}

func (s *Store) UpsertBlob(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error {
	_, err := s.db.ExecContext(ctx, `
		insert into blobs(id, content_hash, size, storage_location, state)
		values ($1, $2, $3, $4, 'available')
		on conflict (id) do update
		set content_hash = excluded.content_hash,
		    size = excluded.size,
		    storage_location = excluded.storage_location,
		    state = excluded.state
	`, blobID, contentHash, size, storageLocation)
	return err
}

func (s *Store) GetBlobByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error) {
	var blob corev1.BlobRecord
	err := s.db.QueryRowContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where id = $1
	`, blobID).Scan(&blob.Id, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &blob, nil
}

func (s *Store) GetBlobsByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where content_hash = any($1)
		order by content_hash
	`, hashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.BlobRecord
	for rows.Next() {
		var blob corev1.BlobRecord
		if err := rows.Scan(&blob.Id, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State); err != nil {
			return nil, err
		}
		out = append(out, &blob)
	}
	return out, rows.Err()
}

func (s *Store) CreateChangeset(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	if req.AuthoringSlice == nil {
		return nil, fmt.Errorf("authoring slice is required")
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = DefaultTargetRef
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		ref, err := s.GetRef(ctx, targetRef)
		if err != nil {
			return nil, err
		}
		baseCommitID = ref.CommitId
	}
	slice, err := s.ResolveSlice(ctx, req.AuthoringSlice)
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
	return s.GetChangeset(ctx, id)
}

func (s *Store) GetChangeset(ctx context.Context, changesetID string) (*corev1.Changeset, error) {
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

func (s *Store) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
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

func (s *Store) SubmitChangeset(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error) {
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

func (s *Store) PublishPending(ctx context.Context, limit int) (int, error) {
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
	rootTreeID, err := s.rootTreeIDForCommitTx(ctx, tx, currentCommitID)
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

func (s *Store) AbandonChangeset(ctx context.Context, changesetID string) error {
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

func (s *Store) GetFile(ctx context.Context, commitID, p string) (*FileEntry, error) {
	rootTreeID, err := s.rootTreeIDForCommit(ctx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getFileFromTree(ctx, rootTreeID, p)
}

func (s *Store) ListFiles(ctx context.Context, commitID, prefix string) ([]FileEntry, error) {
	rootTreeID, err := s.rootTreeIDForCommit(ctx, commitID)
	if err != nil {
		return nil, err
	}
	return s.listFilesFromTree(ctx, rootTreeID, prefix)
}

func (s *Store) rootTreeIDForCommit(ctx context.Context, commitID string) (string, error) {
	var rootTreeID string
	err := s.db.QueryRowContext(ctx, `
		select root_tree_id
		from commits
		where id = $1
	`, commitID).Scan(&rootTreeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return rootTreeID, err
}

func (s *Store) rootTreeIDForCommitTx(ctx context.Context, tx *sql.Tx, commitID string) (string, error) {
	var rootTreeID string
	err := tx.QueryRowContext(ctx, `
		select root_tree_id
		from commits
		where id = $1
	`, commitID).Scan(&rootTreeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return rootTreeID, err
}

func (s *Store) getFileAtCommitTx(ctx context.Context, tx *sql.Tx, commitID, p string) (*FileEntry, error) {
	rootTreeID, err := s.rootTreeIDForCommitTx(ctx, tx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getFileFromTree(ctx, rootTreeID, p)
}

func (s *Store) getFileFromTree(ctx context.Context, rootTreeID, p string) (*FileEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	entry, err := s.trees.GetFile(ctx, rootTreeID, p)
	if errors.Is(err, treestore.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	file := fileEntryFromTree(*entry)
	return &file, nil
}

func (s *Store) listFilesFromTree(ctx context.Context, rootTreeID, prefix string) ([]FileEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	files, err := s.trees.ListFiles(ctx, rootTreeID, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(files))
	for _, file := range files {
		out = append(out, fileEntryFromTree(file))
	}
	return out, nil
}

func (s *Store) CoveringSliceIDs(ctx context.Context, p string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `select id, included_paths from slices order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var prefixes []string
		if err := decodeJSON(raw, &prefixes); err != nil {
			return nil, err
		}
		for _, prefix := range prefixes {
			if p == strings.TrimRight(prefix, "/") || strings.HasPrefix(p, strings.TrimRight(prefix, "/")+"/") {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSlice(row scanner) (*corev1.Slice, error) {
	var (
		id, account, slug, definitionHash, visibility string
		version                                       int64
		includedJSON                                  []byte
	)
	err := row.Scan(&id, &account, &slug, &version, &definitionHash, &visibility, &includedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var included []string
	if err := decodeJSON(includedJSON, &included); err != nil {
		return nil, err
	}
	return &corev1.Slice{
		Id:             id,
		Ref:            &corev1.SliceRef{Account: account, Slice: slug},
		DefinitionHash: definitionHash,
		Definition: &corev1.SliceDefinition{
			SliceId:       id,
			Version:       version,
			IncludedPaths: included,
			Visibility:    visibility,
		},
	}, nil
}

func (s *Store) listPatchsets(ctx context.Context, changesetID string) ([]*corev1.Patchset, error) {
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
		case "delete", "rename":
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

func (s *Store) validateAcceptedPathBaseTx(ctx context.Context, tx *sql.Tx, currentCommitID string, base *corev1.PathBase) error {
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

func (s *Store) getOrInitPathHeadTx(ctx context.Context, tx *sql.Tx, currentCommitID, p string) (*PathHead, error) {
	head, err := getPathHeadTx(ctx, tx, p)
	if err == nil {
		return head, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	entry, err := s.getFileAtCommitTx(ctx, tx, currentCommitID, p)
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
	if err := insertInitialPathHeadTx(ctx, tx, pathHeadFromFile(*entry), "", ""); err != nil {
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

func getBlobTx(ctx context.Context, tx *sql.Tx, blobID string) (*corev1.BlobRecord, error) {
	var blob corev1.BlobRecord
	err := tx.QueryRowContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where id = $1 and state = 'available'
	`, blobID).Scan(&blob.Id, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &blob, nil
}

func FileEntryFingerprint(entry FileEntry) string {
	payload, _ := json.Marshal(struct {
		Kind        string `json:"kind"`
		Mode        uint32 `json:"mode"`
		BlobID      string `json:"blob_id"`
		ContentHash string `json:"content_hash"`
	}{
		Kind:        "file",
		Mode:        entry.Mode,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
	})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fileEntryFromTree(entry treestore.FileEntry) FileEntry {
	return FileEntry{
		Path:        entry.Path,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Mode:        entry.Mode,
		Size:        entry.Size,
	}
}

func fileEntryToTree(entry FileEntry) treestore.FileEntry {
	return treestore.FileEntry{
		Path:        entry.Path,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Mode:        entry.Mode,
		Size:        entry.Size,
	}
}

func MissingEntryFingerprint() string {
	return "missing"
}

func normalizeDevSubject(devUser string) string {
	devUser = strings.TrimSpace(devUser)
	if devUser == "" {
		devUser = "alice"
	}
	devUser = strings.ReplaceAll(devUser, "-", "_")
	if strings.HasPrefix(devUser, "user_") || strings.HasSuffix(devUser, "_bot") {
		return devUser
	}
	return "user_" + devUser
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func definitionHash(sliceID string, version int64, included []string, visibility string) string {
	payload, _ := json.Marshal(struct {
		SliceID    string   `json:"slice_id"`
		Version    int64    `json:"version"`
		Included   []string `json:"included_paths"`
		Visibility string   `json:"visibility"`
	}{SliceID: sliceID, Version: version, Included: included, Visibility: visibility})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func encodeJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func decodeJSON(raw []byte, v any) error {
	if len(raw) == 0 {
		raw = []byte("null")
	}
	return json.Unmarshal(raw, v)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *Store) seedDevFixture(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	parentJSON, err := encodeJSON([]string{})
	if err != nil {
		return err
	}
	changedJSON, err := encodeJSON([]string{})
	if err != nil {
		return err
	}
	rootTreeID := objectid.EmptyTreeID()
	initialCommitID := objectid.CommitID(objectid.CommitObject{
		ParentIDs:    nil,
		RootTreeID:   rootTreeID,
		Author:       "system",
		Message:      "Initial empty tree",
		CreatedAt:    now,
		ChangedPaths: nil,
	})
	if _, err := tx.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, created_at)
		values
			('user_alice', 'user', 'Alice', now()),
			('user_bob', 'user', 'Bob', now()),
			('ci_bot', 'service_account', 'CI Bot', now())
		on conflict (id) do nothing
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into accounts(id, slug, kind, created_at, updated_at)
		values ('acct_acme', 'acme', 'org', now(), now())
		on conflict (id) do nothing
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values
			('acct_acme', 'user_alice', 'admin', now()),
			('acct_acme', 'user_bob', 'writer', now()),
			('acct_acme', 'ci_bot', 'writer', now())
		on conflict do nothing
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into commits(id, parent_ids, root_tree_id, author_subject_id, message, created_at, changed_paths)
		values ($1, $2, $3, 'system', 'Initial empty tree', $4, $5)
		on conflict (id) do nothing
	`, initialCommitID, parentJSON, rootTreeID, now, changedJSON); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into refs(name, commit_id, version, updated_at, updated_by)
		values ($1, $2, 1, now(), 'system')
		on conflict (name) do nothing
	`, DefaultTargetRef, initialCommitID); err != nil {
		return err
	}
	if err := seedSlice(ctx, tx, "slice_acme_payment", "payment", []string{"/acme/payment"}); err != nil {
		return err
	}
	if err := seedSlice(ctx, tx, "slice_acme_backend", "backend", []string{"/acme/backend", "/acme/payment/shared"}); err != nil {
		return err
	}
	return tx.Commit()
}

func seedSlice(ctx context.Context, tx *sql.Tx, id, slug string, included []string) error {
	includedJSON, err := encodeJSON(included)
	if err != nil {
		return err
	}
	returnSQL := `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, created_at, updated_at)
		values ($1, 'acct_acme', $2, 1, $3, 'account', $4, now(), now())
		on conflict (id) do nothing
	`
	_, err = tx.ExecContext(ctx, returnSQL, id, slug, definitionHash(id, 1, included, "account"), includedJSON)
	return err
}

//go:embed migrations/*.sql
var migrationFS embed.FS

func migrationStatements() []string {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		panic(err)
	}
	var statements []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			panic(err)
		}
		for _, stmt := range strings.Split(string(raw), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt != "" {
				statements = append(statements, stmt)
			}
		}
	}
	return statements
}
