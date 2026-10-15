package postgres

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func scanCommitRow(rows *sql.Rows) (*corev1.Commit, error) {
	var commit corev1.Commit
	var parentJSON, changedJSON []byte
	var createdAt time.Time
	if err := rows.Scan(&commit.Id, &parentJSON, &commit.RootTreeId, &commit.Author, &commit.Message, &createdAt, &changedJSON); err != nil {
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

func scanCommitHitRow(rows *sql.Rows) (*corev1.Commit, time.Time, error) {
	var commit corev1.Commit
	var parentJSON, changedJSON []byte
	var createdAt, committedAt time.Time
	if err := rows.Scan(&commit.Id, &parentJSON, &commit.RootTreeId, &commit.Author, &commit.Message, &createdAt, &changedJSON, &committedAt); err != nil {
		return nil, time.Time{}, err
	}
	commit.CreatedAt = formatTime(createdAt)
	if err := decodeJSON(parentJSON, &commit.ParentIds); err != nil {
		return nil, time.Time{}, err
	}
	if err := decodeJSON(changedJSON, &commit.ChangedPaths); err != nil {
		return nil, time.Time{}, err
	}
	return &commit, committedAt, nil
}

func normalizeCommitListLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 50
	}
	return limit
}

func commitPageTokenForCommit(commit *corev1.Commit) (string, error) {
	if commit == nil || commit.Id == "" {
		return "", fmt.Errorf("%w: commit page token cannot be built from empty commit", ErrInvalid)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, commit.CreatedAt)
	if err != nil {
		return "", fmt.Errorf("%w: invalid commit timestamp: %v", ErrInvalid, err)
	}
	return encodeCommitPageToken(commit.Id, createdAt)
}

func encodeCommitPageToken(commitID string, committedAt time.Time) (string, error) {
	if strings.TrimSpace(commitID) == "" || committedAt.IsZero() {
		return "", fmt.Errorf("%w: invalid commit page cursor", ErrInvalid)
	}
	raw, err := json.Marshal(commitPageCursor{
		CommitID:    commitID,
		CommittedAt: committedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCommitPageToken(token string) (*commitPageCursor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	var cursor commitPageCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	if strings.TrimSpace(cursor.CommitID) == "" || strings.TrimSpace(cursor.CommittedAt) == "" {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	if _, err := cursor.committedAt(); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func (c commitPageCursor) committedAt() (time.Time, error) {
	committedAt, err := time.Parse(time.RFC3339Nano, c.CommittedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	return committedAt, nil
}

func normalizeCommitPathPrefixes(prefixes []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(prefix, "/")
		if prefix == "" {
			prefix = "/"
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

func commitPathPrefixWhere(refName string, prefixes []string) (string, []any) {
	args := []any{refName}
	clauses := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix == "/" {
			clauses = append(clauses, "true")
			continue
		}
		exact := len(args) + 1
		pattern := len(args) + 2
		clauses = append(clauses, fmt.Sprintf("(cp.path = $%d or cp.path like $%d escape '\\')", exact, pattern))
		args = append(args, prefix, likePrefixPattern(prefix))
	}
	return "cp.target_ref = $1 and (" + strings.Join(clauses, " or ") + ")", args
}

func currentPathEntityPrefixWhere(refName string, prefixes []string) (string, []any) {
	args := []any{refName}
	clauses := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix == "/" {
			clauses = append(clauses, "true")
			continue
		}
		exact := len(args) + 1
		pattern := len(args) + 2
		clauses = append(clauses, fmt.Sprintf("(cpe.path = $%d or cpe.path like $%d escape '\\')", exact, pattern))
		args = append(args, prefix, likePrefixPattern(prefix))
	}
	return "cpe.target_ref = $1 and (" + strings.Join(clauses, " or ") + ")", args
}

func normalizeHistoryEntityRefs(refs []HistoryEntityRef) []HistoryEntityRef {
	seen := map[string]struct{}{}
	out := make([]HistoryEntityRef, 0, len(refs))
	for _, ref := range refs {
		ref.AccountID = strings.TrimSpace(ref.AccountID)
		ref.EntityID = strings.TrimSpace(ref.EntityID)
		if ref.AccountID == "" || ref.EntityID == "" {
			continue
		}
		key := ref.AccountID + "\x00" + ref.EntityID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AccountID == out[j].AccountID {
			return out[i].EntityID < out[j].EntityID
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

func commitEntityRefWhere(refName string, refs []HistoryEntityRef) (string, []any) {
	args := []any{refName}
	clauses := make([]string, 0, len(refs))
	for _, ref := range refs {
		accountArg := len(args) + 1
		entityArg := len(args) + 2
		clauses = append(clauses, fmt.Sprintf("(ce.account_id = $%d and ce.entity_id = $%d)", accountArg, entityArg))
		args = append(args, ref.AccountID, ref.EntityID)
	}
	return "ce.target_ref = $1 and (" + strings.Join(clauses, " or ") + ")", args
}

func shiftPostgresPlaceholders(query string, offset int) string {
	if offset == 0 {
		return query
	}
	var b strings.Builder
	for i := 0; i < len(query); i++ {
		if query[i] != '$' || i+1 >= len(query) || query[i+1] < '0' || query[i+1] > '9' {
			b.WriteByte(query[i])
			continue
		}
		j := i + 1
		n := 0
		for j < len(query) && query[j] >= '0' && query[j] <= '9' {
			n = n*10 + int(query[j]-'0')
			j++
		}
		b.WriteString(fmt.Sprintf("$%d", n+offset))
		i = j - 1
	}
	return b.String()
}
