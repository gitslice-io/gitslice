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

const (
	cliLoginTTL            = 10 * time.Minute
	cliLoginSessionTTL     = 30 * 24 * time.Hour
	cliLoginTokenPrefix    = "clitok"
	cliLoginStatusPending  = "pending"
	cliLoginStatusApproved = "approved"
	cliLoginStatusExpired  = "expired"
)

type AuthStore struct {
	db    *sql.DB
	trees *treestore.Store
}

func (s *AuthStore) StartCliLogin(ctx context.Context) (string, time.Time, error) {
	code, err := objectid.RandomID("cli")
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(cliLoginTTL)
	if _, err := s.db.ExecContext(ctx, `
		insert into cli_login_sessions(code_hash, status, expires_at)
		values ($1, $2, $3)
	`, tokenHash(code), cliLoginStatusPending, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return code, expiresAt, nil
}

func (s *AuthStore) CompleteCliLogin(ctx context.Context, code, subjectID string) error {
	code = strings.TrimSpace(code)
	subjectID = strings.TrimSpace(subjectID)
	if code == "" || subjectID == "" {
		return ErrNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var codeHash string
	err = tx.QueryRowContext(ctx, `
		select code_hash
		from cli_login_sessions
		where code_hash = $1
		  and status = $2
		  and expires_at > now()
		for update
	`, tokenHash(code), cliLoginStatusPending).Scan(&codeHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	token, sessionID, hashedToken, expiresAt, err := newSessionWithTTL(subjectID, cliLoginTokenPrefix, cliLoginSessionTTL)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into sessions(id, subject_id, token_hash, expires_at)
		values ($1, $2, $3, $4)
	`, sessionID, subjectID, hashedToken, expiresAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		update cli_login_sessions
		set status = $2,
		    subject_id = $3,
		    session_token = $4
		where code_hash = $1
	`, codeHash, cliLoginStatusApproved, subjectID, token); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AuthStore) PollCliLogin(ctx context.Context, code string) (string, string, string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return cliLoginStatusExpired, "", "", nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", "", err
	}
	defer tx.Rollback()

	var status, token, subjectID string
	var expiresAt time.Time
	codeHash := tokenHash(code)
	err = tx.QueryRowContext(ctx, `
		select status, coalesce(session_token, ''), coalesce(subject_id, ''), expires_at
		from cli_login_sessions
		where code_hash = $1
		for update
	`, codeHash).Scan(&status, &token, &subjectID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return cliLoginStatusExpired, "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	if !expiresAt.After(time.Now().UTC()) {
		if _, err := tx.ExecContext(ctx, `delete from cli_login_sessions where code_hash = $1`, codeHash); err != nil {
			return "", "", "", err
		}
		if err := tx.Commit(); err != nil {
			return "", "", "", err
		}
		return cliLoginStatusExpired, "", "", nil
	}
	if status == cliLoginStatusPending {
		if err := tx.Commit(); err != nil {
			return "", "", "", err
		}
		return cliLoginStatusPending, "", "", nil
	}
	if status == cliLoginStatusApproved {
		if _, err := tx.ExecContext(ctx, `delete from cli_login_sessions where code_hash = $1`, codeHash); err != nil {
			return "", "", "", err
		}
		if err := tx.Commit(); err != nil {
			return "", "", "", err
		}
		return cliLoginStatusApproved, token, subjectID, nil
	}
	if err := tx.Commit(); err != nil {
		return "", "", "", err
	}
	return cliLoginStatusExpired, "", "", nil
}

// EnsureExternalSubject idempotently provisions a subject only
// for an externally authenticated identity (for example a verified Clerk user).
// Unlike SignupUser it issues no session token: the external provider's token is
// verified on every request, so there is no internal session to mint.
func (s *AuthStore) EnsureExternalSubject(ctx context.Context, externalID, email string) (string, error) {
	subjectID := storage.ExternalSubjectID(externalID)
	displayName := strings.TrimSpace(email)
	if displayName == "" {
		displayName = subjectID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, created_at)
		values ($1, 'user', $2, now())
		on conflict (id) do nothing
	`, subjectID, displayName); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return subjectID, nil
}

// provisionAccountForSubject creates the personal account, admin membership,
// home slice (+ definition version + path index) and account-root directory for
// an existing subjectID under the chosen username, within tx. It backs
// ChooseUsername (provisioning the account for a pre-existing external subject).
func (s *AuthStore) provisionAccountForSubject(ctx context.Context, tx *sql.Tx, subjectID, username, displayName string) (accountID string, err error) {
	accountID = signupAccountID(username)

	var existingAccountID, existingKind string
	err = tx.QueryRowContext(ctx, `
		select id, kind
		from accounts
		where slug = $1
	`, username).Scan(&existingAccountID, &existingKind)
	if errors.Is(err, sql.ErrNoRows) {
		res, insertErr := tx.ExecContext(ctx, `
			insert into accounts(id, slug, kind, created_at, updated_at)
			values ($1, $2, 'personal', now(), now())
			on conflict (slug) do nothing
		`, accountID, username)
		if insertErr != nil {
			return "", insertErr
		}
		affected, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return "", rowsErr
		}
		if affected == 0 {
			err = tx.QueryRowContext(ctx, `
				select id, kind
				from accounts
				where slug = $1
			`, username).Scan(&existingAccountID, &existingKind)
		} else {
			err = nil
		}
	}
	if err != nil {
		return "", err
	}
	if existingAccountID != "" {
		if existingKind != "personal" {
			return "", fmt.Errorf("%w: username %q is not available", ErrConflict, username)
		}
		var owns bool
		if err := tx.QueryRowContext(ctx, `
			select exists(
				select 1
				from account_memberships
				where account_id = $1 and subject_id = $2
			)
		`, existingAccountID, subjectID).Scan(&owns); err != nil {
			return "", err
		}
		if !owns {
			return "", fmt.Errorf("%w: username %q is not available", ErrConflict, username)
		}
		accountID = existingAccountID
	}

	if _, err := tx.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values ($1, $2, 'admin', now())
		on conflict do nothing
	`, accountID, subjectID); err != nil {
		return "", err
	}

	homeSliceID := signupHomeSliceID(username)
	homeIncludedPaths := []string{"/" + username}
	homeIncludedJSON, err := encodeJSON(homeIncludedPaths)
	if err != nil {
		return "", err
	}
	emptyChecksJSON, err := encodeJSON([]string{})
	if err != nil {
		return "", err
	}
	homeDefinitionHash := definitionHash(homeSliceID, 1, homeIncludedPaths, "private", 0, nil)
	if _, err := tx.ExecContext(ctx, `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, created_at, updated_at)
		values ($1, $2, 'home', 1, $3, 'private', $4, now(), now())
		on conflict (account_id, slug) do nothing
	`, homeSliceID, accountID, homeDefinitionHash, homeIncludedJSON); err != nil {
		return "", err
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
		values ($1, 1, $2, 'private', $3, 0, $4, now(), $5)
		on conflict do nothing
	`, homeSliceID, homeDefinitionHash, homeIncludedJSON, emptyChecksJSON, subjectID); err != nil {
		return "", err
	}
	if err := syncSliceIncludedPathsTx(ctx, tx, homeSliceID, homeIncludedPaths); err != nil {
		return "", err
	}
	if err := ensureAccountRootDirectoryTx(ctx, tx, username, subjectID, s.trees); err != nil {
		return "", err
	}

	return accountID, nil
}

func (s *AuthStore) UsernameAvailable(ctx context.Context, username string) (bool, string, string, error) {
	normalized, err := normalizeSignupUsername(username)
	if err != nil {
		return false, "", err.Error(), nil
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `
		select exists(select 1 from accounts where slug = $1)
	`, normalized).Scan(&exists); err != nil {
		return false, "", "", err
	}
	if exists {
		return false, normalized, "username is taken", nil
	}
	return true, normalized, "", nil
}

func (s *AuthStore) ChooseUsername(ctx context.Context, subjectID, username string) (string, error) {
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return "", fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	username, err := normalizeSignupUsername(username)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var foundSubjectID string
	err = tx.QueryRowContext(ctx, `
		select id
		from subjects
		where id = $1
		for update
	`, subjectID).Scan(&foundSubjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}

	var existingSlug string
	err = tx.QueryRowContext(ctx, `
		select a.slug
		from account_memberships m
		join accounts a on a.id = m.account_id
		where m.subject_id = $1 and a.kind = 'personal'
		order by a.slug
		limit 1
	`, subjectID).Scan(&existingSlug)
	if err == nil {
		return existingSlug, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	if _, err := s.provisionAccountForSubject(ctx, tx, subjectID, username, username); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return username, nil
}

func (s *AuthStore) UsernamesForSubjects(ctx context.Context, subjectIDs []string) (map[string]string, error) {
	ids := make([]string, 0, len(subjectIDs))
	seen := map[string]struct{}{}
	for _, subjectID := range subjectIDs {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" {
			continue
		}
		if _, ok := seen[subjectID]; ok {
			continue
		}
		seen[subjectID] = struct{}{}
		ids = append(ids, subjectID)
	}
	if len(ids) == 0 {
		return map[string]string{}, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		select m.subject_id, a.slug
		from account_memberships m
		join accounts a on a.id = m.account_id
		where a.kind = 'personal' and m.subject_id = any($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var subjectID, slug string
		if err := rows.Scan(&subjectID, &slug); err != nil {
			return nil, err
		}
		out[subjectID] = slug
	}
	return out, rows.Err()
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

func newSessionWithTTL(subjectID, tokenPrefix string, ttl time.Duration) (token, sessionID, hashedToken string, expiresAt time.Time, err error) {
	token, err = objectid.RandomID(tokenPrefix)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	sessionID, err = objectid.RandomID("sess")
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	return token, sessionID, tokenHash(token), time.Now().UTC().Add(ttl), nil
}
