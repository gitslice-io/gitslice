package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/objectid"
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
	db *sql.DB
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

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	for _, stmt := range migrationStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
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
	err := s.db.QueryRowContext(ctx, `
		select name, commit_id, updated_at, coalesce(updated_by, '')
		from refs
		where name = $1
	`, name).Scan(&ref.Name, &ref.CommitID, &ref.UpdatedAt, &ref.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func (s *Store) GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error) {
	var commit corev1.Commit
	var parentJSON, changedJSON []byte
	err := s.db.QueryRowContext(ctx, `
		select id, parent_ids, root_tree_id, coalesce(author_subject_id, ''),
		       message, created_at, changed_paths
		from commits
		where id = $1
	`, commitID).Scan(&commit.ID, &parentJSON, &commit.RootTreeID, &commit.Author, &commit.Message, &commit.CreatedAt, &changedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := decodeJSON(parentJSON, &commit.ParentIDs); err != nil {
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
	`, blobID).Scan(&blob.ID, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State)
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
		if err := rows.Scan(&blob.ID, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State); err != nil {
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
	baseCommitID := req.BaseCommitID
	if baseCommitID == "" {
		ref, err := s.GetRef(ctx, targetRef)
		if err != nil {
			return nil, err
		}
		baseCommitID = ref.CommitID
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
	`, id, req.AuthoringSlice.Account, req.AuthoringSlice.Slice, slice.ID, subjectID,
		targetRef, baseCommitID, req.Title, req.Description, empty, now)
	if err != nil {
		return nil, err
	}
	return s.GetChangeset(ctx, id)
}

func (s *Store) GetChangeset(ctx context.Context, changesetID string) (*corev1.Changeset, error) {
	var cs corev1.Changeset
	var account, slice, currentPatchsetID sql.NullString
	var affectedJSON []byte
	err := s.db.QueryRowContext(ctx, `
		select id, authoring_account, authoring_slice, author_subject_id, target_ref,
		       base_commit_id, title, description, status, affected_paths,
		       coalesce(current_patchset_number, 0), current_patchset_id
		from changesets
		where id = $1
	`, changesetID).Scan(&cs.ID, &account, &slice, &cs.Author, &cs.TargetRef,
		&cs.BaseCommitID, &cs.Title, &cs.Description, &cs.Status, &affectedJSON,
		&cs.CurrentPatchsetNumber, &currentPatchsetID)
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
		cs.CurrentPatchsetID = currentPatchsetID.String
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
	patchset.ID = patchsetID
	patchset.ChangesetID = changesetID
	patchset.Number = currentNumber + 1
	patchset.CreatedAt = time.Now().UTC()
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
	`, patchset.ID, changesetID, patchset.Number, patchset.BaseCommitID, patchset.Author,
		fileEditsJSON, changedJSON, coverageJSON, pathBasesJSON, readSetJSON, writeSetJSON, patchset.CreatedAt)
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
	`, patchset.ID, patchset.Number, changedJSON, changesetID)
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
		return &corev1.SubmitChangesetResponse{CommitID: cs.CommitID.String, TargetRef: cs.TargetRef, NewRefCommitID: cs.CommitID.String}, nil
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
		for update
	`, cs.TargetRef).Scan(&currentCommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	for _, base := range patchset.PathBases {
		if base.BaseCommitID != currentCommitID {
			return nil, ErrConflict
		}
	}
	files, err := loadCommitFilesTx(ctx, tx, currentCommitID)
	if err != nil {
		return nil, err
	}
	if err := applyFileEditsTx(ctx, tx, files, patchset.FileEdits); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rootTreeID := rootTreeID(files)
	message := cs.Title
	if message == "" {
		message = "Submit " + cs.ID
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
		return nil, err
	}
	changedJSON, err := encodeJSON(patchset.ChangedPaths)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		insert into commits(id, parent_ids, root_tree_id, author_subject_id, message, created_at, changed_paths)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (id) do nothing
	`, commitID, parentJSON, rootTreeID, cs.Author, message, now, changedJSON)
	if err != nil {
		return nil, err
	}
	if err := insertCommitFilesTx(ctx, tx, commitID, files); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		update refs
		set commit_id = $1, version = version + 1, updated_at = now(), updated_by = $2
		where name = $3 and commit_id = $4
	`, commitID, cs.Author, cs.TargetRef, currentCommitID)
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
	_, err = tx.ExecContext(ctx, `
		update changesets
		set status = 'submitted', commit_id = $1, updated_at = now()
		where id = $2
	`, commitID, cs.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.SubmitChangesetResponse{CommitID: commitID, TargetRef: cs.TargetRef, NewRefCommitID: commitID}, nil
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
	var entry FileEntry
	err := s.db.QueryRowContext(ctx, `
		select path, blob_id, content_hash, mode, size
		from commit_files
		where commit_id = $1 and path = $2
	`, commitID, p).Scan(&entry.Path, &entry.BlobID, &entry.ContentHash, &entry.Mode, &entry.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func (s *Store) ListFiles(ctx context.Context, commitID, prefix string) ([]FileEntry, error) {
	likePrefix := strings.TrimRight(prefix, "/")
	var rows *sql.Rows
	var err error
	if likePrefix == "" || likePrefix == "/" {
		rows, err = s.db.QueryContext(ctx, `
			select path, blob_id, content_hash, mode, size
			from commit_files
			where commit_id = $1
			order by path
		`, commitID)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			select path, blob_id, content_hash, mode, size
			from commit_files
			where commit_id = $1 and (path = $2 or path like $3)
			order by path
		`, commitID, likePrefix, likePrefix+"/%")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileEntry
	for rows.Next() {
		var entry FileEntry
		if err := rows.Scan(&entry.Path, &entry.BlobID, &entry.ContentHash, &entry.Mode, &entry.Size); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
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
		ID:             id,
		Ref:            &corev1.SliceRef{Account: account, Slice: slug},
		DefinitionHash: definitionHash,
		Definition: &corev1.SliceDefinition{
			SliceID:       id,
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
	err := row.Scan(&patchset.ID, &patchset.ChangesetID, &patchset.Number, &patchset.BaseCommitID,
		&patchset.Author, &patchset.CreatedAt, &changedJSON, &fileEditsJSON, &coverageJSON,
		&pathBasesJSON, &readSetJSON, &writeSetJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
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

func loadCommitFilesTx(ctx context.Context, tx *sql.Tx, commitID string) (map[string]FileEntry, error) {
	rows, err := tx.QueryContext(ctx, `
		select path, blob_id, content_hash, mode, size
		from commit_files
		where commit_id = $1
	`, commitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := map[string]FileEntry{}
	for rows.Next() {
		var entry FileEntry
		if err := rows.Scan(&entry.Path, &entry.BlobID, &entry.ContentHash, &entry.Mode, &entry.Size); err != nil {
			return nil, err
		}
		files[entry.Path] = entry
	}
	return files, rows.Err()
}

func applyFileEditsTx(ctx context.Context, tx *sql.Tx, files map[string]FileEntry, edits []*corev1.FileEdit) error {
	for _, edit := range edits {
		switch edit.Op {
		case "delete":
			delete(files, edit.Path)
		case "rename":
			entry, ok := files[edit.OldPath]
			if !ok {
				return ErrConflict
			}
			delete(files, edit.OldPath)
			entry.Path = edit.Path
			files[edit.Path] = entry
		default:
			blob, err := getBlobTx(ctx, tx, edit.BlobID)
			if err != nil {
				return err
			}
			files[edit.Path] = FileEntry{
				Path:        edit.Path,
				BlobID:      blob.ID,
				ContentHash: blob.ContentHash,
				Mode:        edit.Mode,
				Size:        blob.Size,
			}
		}
	}
	return nil
}

func getBlobTx(ctx context.Context, tx *sql.Tx, blobID string) (*corev1.BlobRecord, error) {
	var blob corev1.BlobRecord
	err := tx.QueryRowContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where id = $1 and state = 'available'
	`, blobID).Scan(&blob.ID, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &blob, nil
}

func insertCommitFilesTx(ctx context.Context, tx *sql.Tx, commitID string, files map[string]FileEntry) error {
	if len(files) == 0 {
		return nil
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		entry := files[p]
		_, err := tx.ExecContext(ctx, `
			insert into commit_files(commit_id, path, blob_id, content_hash, mode, size)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (commit_id, path) do nothing
		`, commitID, entry.Path, entry.BlobID, entry.ContentHash, entry.Mode, entry.Size)
		if err != nil {
			return err
		}
	}
	return nil
}

func rootTreeID(files map[string]FileEntry) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	entries := make([]objectid.TreeEntry, 0, len(paths))
	for _, p := range paths {
		entry := files[p]
		entries = append(entries, objectid.TreeEntry{
			Name:        p,
			Kind:        "file",
			Mode:        entry.Mode,
			BlobID:      entry.BlobID,
			ContentHash: entry.ContentHash,
		})
	}
	return objectid.TreeID(entries)
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

var migrationStatements = []string{
	`create table if not exists accounts(
		id text primary key,
		slug text unique not null,
		kind text not null,
		created_at timestamptz not null default now(),
		updated_at timestamptz not null default now()
	)`,
	`create table if not exists subjects(
		id text primary key,
		kind text not null,
		external_provider text,
		external_subject text,
		display_name text not null,
		created_at timestamptz not null default now()
	)`,
	`create table if not exists account_memberships(
		account_id text not null references accounts(id),
		subject_id text not null references subjects(id),
		role text not null,
		created_at timestamptz not null default now(),
		primary key(account_id, subject_id, role)
	)`,
	`create table if not exists sessions(
		id text primary key,
		subject_id text not null references subjects(id),
		token_hash text unique not null,
		expires_at timestamptz not null,
		revoked_at timestamptz,
		created_at timestamptz not null default now()
	)`,
	`create table if not exists refs(
		name text primary key,
		commit_id text not null,
		version bigint not null default 0,
		updated_at timestamptz not null default now(),
		updated_by text
	)`,
	`create table if not exists commits(
		id text primary key,
		parent_ids jsonb not null,
		root_tree_id text not null,
		author_subject_id text,
		message text not null,
		created_at timestamptz not null,
		changed_paths jsonb not null,
		metadata jsonb not null default '{}'::jsonb
	)`,
	`create table if not exists commit_files(
		commit_id text not null references commits(id),
		path text not null,
		blob_id text not null,
		content_hash text not null,
		mode integer not null,
		size bigint not null,
		primary key(commit_id, path)
	)`,
	`create table if not exists blobs(
		id text primary key,
		content_hash text unique not null,
		size bigint not null,
		storage_location text not null,
		state text not null,
		created_at timestamptz not null default now()
	)`,
	`create table if not exists slices(
		id text primary key,
		account_id text not null references accounts(id),
		slug text not null,
		version bigint not null,
		definition_hash text not null,
		visibility text not null,
		included_paths jsonb not null,
		created_at timestamptz not null default now(),
		updated_at timestamptz not null default now(),
		unique(account_id, slug)
	)`,
	`create table if not exists changesets(
		id text primary key,
		authoring_account text not null,
		authoring_slice text not null,
		authoring_slice_id text not null references slices(id),
		author_subject_id text not null references subjects(id),
		target_ref text not null references refs(name),
		base_commit_id text not null,
		title text not null,
		description text not null,
		status text not null,
		affected_paths jsonb not null,
		current_patchset_id text,
		current_patchset_number bigint not null default 0,
		commit_id text,
		created_at timestamptz not null default now(),
		updated_at timestamptz not null default now()
	)`,
	`create table if not exists patchsets(
		id text primary key,
		changeset_id text not null references changesets(id),
		number bigint not null,
		base_commit_id text not null,
		author_subject_id text not null references subjects(id),
		file_edits jsonb not null,
		changed_paths jsonb not null,
		coverage jsonb not null,
		path_bases jsonb not null,
		read_set jsonb not null,
		write_set jsonb not null,
		created_at timestamptz not null default now(),
		unique(changeset_id, number)
	)`,
	`create index if not exists idx_sessions_token_hash on sessions(token_hash) where revoked_at is null`,
	`create index if not exists idx_slices_account_slug on slices(account_id, slug)`,
	`create index if not exists idx_commit_files_path on commit_files(commit_id, path)`,
	`create index if not exists idx_changesets_target_status on changesets(target_ref, status)`,
	`create index if not exists idx_patchsets_changeset_number on patchsets(changeset_id, number desc)`,
	`create index if not exists idx_blobs_content_hash on blobs(content_hash)`,
}
