package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/treestore"
)

type AuthStore struct {
	db    *sql.DB
	trees *treestore.Store
}

func (s *AuthStore) LoginDevUser(ctx context.Context, devUser string) (string, string, error) {
	subjectID := normalizeDevSubject(devUser)
	var found string
	err := s.db.QueryRowContext(ctx, `select id from subjects where id = $1`, subjectID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	token, sessionID, hashedToken, expiresAt, err := newSession(subjectID)
	if err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx, `
		insert into sessions(id, subject_id, token_hash, expires_at)
		values ($1, $2, $3, $4)
	`, sessionID, subjectID, hashedToken, expiresAt)
	if err != nil {
		return "", "", err
	}
	return token, subjectID, nil
}

func (s *AuthStore) SignupUser(ctx context.Context, username string) (string, string, error) {
	username, err := normalizeSignupUsername(username)
	if err != nil {
		return "", "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	subjectID, _, err := s.provisionPersonalAccount(ctx, tx, username, username)
	if err != nil {
		return "", "", err
	}

	token, sessionID, hashedToken, expiresAt, err := newSession(subjectID)
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into sessions(id, subject_id, token_hash, expires_at)
		values ($1, $2, $3, $4)
	`, sessionID, subjectID, hashedToken, expiresAt); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return token, subjectID, nil
}

// EnsureExternalSubject idempotently provisions a subject and personal account
// for an externally authenticated identity (for example a verified Clerk user).
// Unlike SignupUser it issues no session token: the external provider's token is
// verified on every request, so there is no internal session to mint.
func (s *AuthStore) EnsureExternalSubject(ctx context.Context, externalID, email string) (string, error) {
	username := storage.ExternalUsername(externalID, email)
	displayName := strings.TrimSpace(email)
	if displayName == "" {
		displayName = username
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	subjectID, _, err := s.provisionPersonalAccount(ctx, tx, username, displayName)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return subjectID, nil
}

// provisionPersonalAccount idempotently creates the subject, personal account,
// admin membership, and home slice (with its definition version and path index)
// for username within tx, returning the subject and account IDs. The caller owns
// the transaction lifecycle.
func (s *AuthStore) provisionPersonalAccount(ctx context.Context, tx *sql.Tx, username, displayName string) (string, string, error) {
	subjectID := signupSubjectID(username)
	accountID := signupAccountID(username)

	if _, err := tx.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, created_at)
		values ($1, 'user', $2, now())
		on conflict (id) do nothing
	`, subjectID, displayName); err != nil {
		return "", "", err
	}

	var existingAccountID, existingKind string
	err := tx.QueryRowContext(ctx, `
		select id, kind
		from accounts
		where slug = $1
	`, username).Scan(&existingAccountID, &existingKind)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			insert into accounts(id, slug, kind, created_at, updated_at)
			values ($1, $2, 'personal', now(), now())
		`, accountID, username)
	}
	if err != nil {
		return "", "", err
	}
	if existingAccountID != "" && (existingAccountID != accountID || existingKind != "personal") {
		return "", "", fmt.Errorf("%w: username %q is not available", ErrConflict, username)
	}

	if _, err := tx.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values ($1, $2, 'admin', now())
		on conflict do nothing
	`, accountID, subjectID); err != nil {
		return "", "", err
	}

	homeSliceID := signupHomeSliceID(username)
	homeIncludedPaths := []string{"/" + username}
	homeIncludedJSON, err := encodeJSON(homeIncludedPaths)
	if err != nil {
		return "", "", err
	}
	emptyChecksJSON, err := encodeJSON([]string{})
	if err != nil {
		return "", "", err
	}
	homeDefinitionHash := definitionHash(homeSliceID, 1, homeIncludedPaths, "account", 0, nil)
	if _, err := tx.ExecContext(ctx, `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, created_at, updated_at)
		values ($1, $2, 'home', 1, $3, 'account', $4, now(), now())
		on conflict (account_id, slug) do nothing
	`, homeSliceID, accountID, homeDefinitionHash, homeIncludedJSON); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into slice_definition_versions(
			slice_id,
			version,
			definition_hash,
			visibility,
			included_paths,
			required_approvals,
			required_checks,
			created_at,
			created_by
		)
		values ($1, 1, $2, 'account', $3, 0, $4, now(), $5)
		on conflict do nothing
	`, homeSliceID, homeDefinitionHash, homeIncludedJSON, emptyChecksJSON, subjectID); err != nil {
		return "", "", err
	}
	if err := syncSliceIncludedPathsTx(ctx, tx, homeSliceID, homeIncludedPaths); err != nil {
		return "", "", err
	}
	if err := ensureAccountRootDirectoryTx(ctx, tx, username, subjectID, s.trees); err != nil {
		return "", "", err
	}

	return subjectID, accountID, nil
}

func ensureAccountRootDirectoryTx(ctx context.Context, tx *sql.Tx, accountSlug, subjectID string, trees *treestore.Store) error {
	accountSlug = strings.TrimSpace(accountSlug)
	if accountSlug == "" {
		return fmt.Errorf("%w: account slug is required", ErrInvalid)
	}
	if trees == nil {
		return nil
	}

	accountRoot := "/" + accountSlug
	var currentCommitID string
	err := tx.QueryRowContext(ctx, `
		select commit_id
		from refs
		where name = $1
		for update
	`, DefaultTargetRef).Scan(&currentCommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	rootTreeID, err := rootTreeIDForCommitTx(ctx, tx, currentCommitID)
	if err != nil {
		return err
	}
	if _, err := trees.GetEntry(ctx, rootTreeID, accountRoot); err == nil {
		return nil
	} else if !errors.Is(err, treestore.ErrNotFound) {
		return err
	}

	newRootTreeID, err := trees.ApplyEdits(ctx, rootTreeID, []treestore.FileEdit{
		{Op: "mkdir", Path: accountRoot},
	})
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	commitID := objectid.CommitID(objectid.CommitObject{
		ParentIDs:    []string{currentCommitID},
		RootTreeID:   newRootTreeID,
		Author:       subjectID,
		Message:      "Create account root " + accountRoot,
		CreatedAt:    now,
		ChangedPaths: []string{accountRoot},
	})
	parentJSON, err := encodeJSON([]string{currentCommitID})
	if err != nil {
		return err
	}
	changedJSON, err := encodeJSON([]string{accountRoot})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into commits(id, parent_ids, root_tree_id, author_subject_id, message, created_at, changed_paths)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (id) do nothing
	`, commitID, parentJSON, newRootTreeID, subjectID, "Create account root "+accountRoot, now, changedJSON); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		update refs
		set commit_id = $1, version = version + 1, updated_at = $2, updated_by = $3
		where name = $4 and commit_id = $5
	`, commitID, now, subjectID, DefaultTargetRef, currentCommitID)
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
	if err := insertCommitChangedPathsTx(ctx, tx, DefaultTargetRef, commitID, []string{accountRoot}, now); err != nil {
		return err
	}
	entry, err := trees.GetEntry(ctx, newRootTreeID, accountRoot)
	if err != nil {
		return err
	}
	return upsertPathHeadTx(ctx, tx, pathHeadFromTreeEntry(treeEntryFromTree(*entry)), "", "")
}

func (s *AuthStore) SubjectForToken(ctx context.Context, token string) (*Subject, error) {
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

func (s *AuthStore) EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error {
	_, err := s.AccountRole(ctx, subjectID, accountSlug)
	return err
}

func (s *AuthStore) AccountRole(ctx context.Context, subjectID, accountSlug string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `
		select m.role
		from account_memberships m
		join accounts a on a.id = m.account_id
		where a.slug = $1 and m.subject_id = $2
		order by case m.role
			when 'owner' then 1
			when 'admin' then 2
			when 'writer' then 3
			when 'member' then 4
			when 'reader' then 5
			when 'guest' then 6
			else 7
		end
		limit 1
	`, accountSlug, subjectID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

func (s *AuthStore) ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		select a.slug
		from account_memberships m
		join accounts a on a.id = m.account_id
		where m.subject_id = $1
		order by
			case
				when a.kind = 'personal' and $1 = 'user_' || replace(a.slug, '-', '_') then 0
				else 1
			end,
			a.slug
	`, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}

func newSession(subjectID string) (token, sessionID, hashedToken string, expiresAt time.Time, err error) {
	token, err = objectid.RandomID("devtok")
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	sessionID, err = objectid.RandomID("sess")
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	return token, sessionID, tokenHash(token), time.Now().UTC().Add(24 * time.Hour), nil
}
