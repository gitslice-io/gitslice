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
)

// EnsureExternalSubject idempotently provisions a subject only
// for an externally authenticated identity (for example a verified Clerk user).
// Unlike SignupUser it issues no session token: the external provider's token is
// verified on every request, so there is no internal session to mint.
func (s *AuthStore) EnsureExternalSubject(ctx context.Context, provider, externalID, email string) (string, error) {
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

	// Record the provider identity once; the conditional update keeps this a
	// no-op write on the per-request hot path after the first sign-in.
	if _, err := tx.ExecContext(ctx, `
		insert into subjects(id, kind, external_provider, external_subject, display_name, created_at)
		values ($1, 'user', nullif($3, ''), nullif($4, ''), $2, now())
		on conflict (id) do update
		set external_provider = excluded.external_provider,
		    external_subject = excluded.external_subject
		where subjects.external_subject is null and excluded.external_subject is not null
	`, subjectID, displayName, strings.TrimSpace(provider), strings.TrimSpace(externalID)); err != nil {
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
				from account_memberships m
				join accounts a on a.id = m.account_id
				where m.account_id = $1 and m.subject_id = $2
				  and `+notOthersAgentAccountSQL+`
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

	if err := s.provisionHomeTx(ctx, tx, accountID, username, subjectID); err != nil {
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
		  and `+notOthersAgentAccountSQL+`
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
		  and `+notOthersAgentAccountSQL+`
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

// provisionHomeTx creates an account's private home slice covering /<slug>
// (with its first definition version) and the account root directory. It is
// idempotent, and shared by personal and organization provisioning.
func (s *AuthStore) provisionHomeTx(ctx context.Context, tx *sql.Tx, accountID, slug, createdBy string) error {
	homeSliceID := signupHomeSliceID(slug)
	homeIncludedPaths := []string{"/" + slug}
	homeIncludedJSON, err := encodeJSON(homeIncludedPaths)
	if err != nil {
		return err
	}
	emptyChecksJSON, err := encodeJSON([]string{})
	if err != nil {
		return err
	}
	homeDefinitionHash := definitionHash(homeSliceID, 1, homeIncludedPaths, "private", 0, nil)
	if _, err := tx.ExecContext(ctx, `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, created_at, updated_at)
		values ($1, $2, 'home', 1, $3, 'private', $4, now(), now())
		on conflict (account_id, slug) do nothing
	`, homeSliceID, accountID, homeDefinitionHash, homeIncludedJSON); err != nil {
		return err
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
	`, homeSliceID, homeDefinitionHash, homeIncludedJSON, emptyChecksJSON, createdBy); err != nil {
		return err
	}
	if err := syncSliceIncludedPathsTx(ctx, tx, homeSliceID, homeIncludedPaths); err != nil {
		return err
	}
	if err := ensureAccountRootDirectoryTx(ctx, tx, slug, createdBy, s.trees); err != nil {
		return err
	}
	return nil
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
