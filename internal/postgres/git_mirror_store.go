package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

// GitMirrorStore keeps slice Git mirrors (design/35_git_mirrors.md).
type GitMirrorStore struct {
	db *sql.DB
}

const gitMirrorColumns = `m.slice_id, accounts.slug, slices.slug, m.url, m.branch, m.username, m.token_secret, m.created_by,
	m.created_at, m.updated_at, m.pending_seq, m.pushed_seq, m.next_attempt_at, m.lease_until, m.failures,
	m.last_attempt_at, m.last_pushed_at, m.last_pushed_commit, m.last_pushed_native, m.last_error`

const gitMirrorFrom = ` from slice_git_mirrors m
	join slices on slices.id = m.slice_id and slices.deleted_at is null
	join accounts on accounts.id = slices.account_id`

func scanGitMirror(row rowScanner) (*storage.GitMirror, error) {
	var m storage.GitMirror
	var lease, attempted, pushed sql.NullTime
	if err := row.Scan(&m.SliceID, &m.Account, &m.Slice, &m.URL, &m.Branch, &m.Username, &m.TokenSecret, &m.CreatedBy,
		&m.CreatedAt, &m.UpdatedAt, &m.PendingSeq, &m.PushedSeq, &m.NextAttemptAt, &lease, &m.Failures,
		&attempted, &pushed, &m.LastPushedCommit, &m.LastPushedNative, &m.LastError); err != nil {
		return nil, err
	}
	m.LeaseUntil, m.LastAttemptAt, m.LastPushedAt = lease.Time, attempted.Time, pushed.Time
	return &m, nil
}

func (s *GitMirrorStore) GetGitMirror(ctx context.Context, sliceID string) (*storage.GitMirror, error) {
	m, err := scanGitMirror(s.db.QueryRowContext(ctx, `select `+gitMirrorColumns+gitMirrorFrom+` where m.slice_id = $1`, strings.TrimSpace(sliceID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	return m, err
}

func (s *GitMirrorStore) PutGitMirror(ctx context.Context, m storage.GitMirror) (*storage.GitMirror, error) {
	if strings.TrimSpace(m.SliceID) == "" || strings.TrimSpace(m.URL) == "" {
		return nil, fmt.Errorf("%w: slice and url are required", ErrInvalid)
	}
	if m.Branch == "" {
		m.Branch = "main"
	}
	if _, err := s.db.ExecContext(ctx, `
		insert into slice_git_mirrors(slice_id, url, branch, username, token_secret, created_by)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (slice_id) do update set
			url = excluded.url, branch = excluded.branch, username = excluded.username,
			token_secret = excluded.token_secret, created_by = excluded.created_by, updated_at = now(),
			pending_seq = slice_git_mirrors.pending_seq + 1, next_attempt_at = now(), failures = 0, last_error = ''
	`, m.SliceID, m.URL, m.Branch, m.Username, m.TokenSecret, m.CreatedBy); err != nil {
		return nil, err
	}
	return s.GetGitMirror(ctx, m.SliceID)
}

func (s *GitMirrorStore) DeleteGitMirror(ctx context.Context, sliceID string) error {
	_, err := s.db.ExecContext(ctx, `delete from slice_git_mirrors where slice_id = $1`, strings.TrimSpace(sliceID))
	return err
}

func (s *GitMirrorStore) ListGitMirrors(ctx context.Context) ([]storage.GitMirror, error) {
	rows, err := s.db.QueryContext(ctx, `select `+gitMirrorColumns+gitMirrorFrom+` order by accounts.slug, slices.slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.GitMirror
	for rows.Next() {
		m, err := scanGitMirror(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *GitMirrorStore) MarkGitMirrorsPending(ctx context.Context, sliceIDs []string, now bool) error {
	if len(sliceIDs) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		update slice_git_mirrors set pending_seq = pending_seq + 1,
			next_attempt_at = case when $2 then now() else next_attempt_at end,
			failures = case when $2 then 0 else failures end
		where slice_id = any($1)
	`, sliceIDs, now)
	return err
}

func (s *GitMirrorStore) ClaimDueGitMirrors(ctx context.Context, limit int, lease time.Duration) ([]storage.GitMirror, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
		with due as (
			select slice_id from slice_git_mirrors
			where pending_seq > pushed_seq and next_attempt_at <= now()
				and (lease_until is null or lease_until < now())
			order by next_attempt_at
			limit $1
			for update skip locked
		)
		update slice_git_mirrors m set lease_until = now() + make_interval(secs => $2)
		from due where m.slice_id = due.slice_id
		returning m.slice_id
	`, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []storage.GitMirror
	for _, id := range ids {
		m, err := s.GetGitMirror(ctx, id)
		if errors.Is(err, storage.ErrNotFound) {
			continue // its slice was deleted
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, nil
}

func (s *GitMirrorStore) FinishGitMirror(ctx context.Context, sliceID string, push storage.GitMirrorPush) error {
	at := push.At
	if at.IsZero() {
		at = time.Now()
	}
	if push.OK {
		_, err := s.db.ExecContext(ctx, `
			update slice_git_mirrors set lease_until = null, last_attempt_at = $2,
				pushed_seq = greatest(pushed_seq, $3), failures = 0, last_error = '', next_attempt_at = $2,
				last_pushed_at = case when $4 <> '' then $2 else last_pushed_at end,
				last_pushed_commit = case when $4 <> '' then $4 else last_pushed_commit end,
				last_pushed_native = case when $4 <> '' then $5 else last_pushed_native end
			where slice_id = $1
		`, sliceID, at, push.Seq, push.Commit, push.Native)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		update slice_git_mirrors set lease_until = null, last_attempt_at = $2,
			failures = failures + 1, last_error = $3, next_attempt_at = $4
		where slice_id = $1
	`, sliceID, at, push.Error, push.NextAttemptAt)
	return err
}
