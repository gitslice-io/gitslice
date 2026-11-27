package postgres

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
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
	return storage.NormalizeCommitListLimit(limit)
}

func commitPageTokenForCommit(commit *corev1.Commit) (string, error) {
	return storage.CommitPageTokenForCommit(commit)
}

func encodeCommitPageToken(commitID string, committedAt time.Time) (string, error) {
	return storage.EncodeCommitPageToken(commitID, committedAt)
}

func decodeCommitPageToken(token string) (*storage.CommitPageCursor, error) {
	return storage.DecodeCommitPageToken(token)
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

// commitEntityRefWhere selects the changes of the given entities on refName.
// The entities go in as two arrays joined with unnest, three parameters
// however many there are. It used to be one (account = and entity =) clause per
// entity, joined with OR: a slice of 5,500 files (a moved import) became a
// 5,500-term OR with 11,000 parameters, which ran the database out of memory
// (SQLSTATE 53200) and took the slice's history page down.
func commitEntityRefWhere(refName string, refs []HistoryEntityRef) (string, []any) {
	accounts := make([]string, 0, len(refs))
	entities := make([]string, 0, len(refs))
	for _, ref := range refs {
		accounts = append(accounts, ref.AccountID)
		entities = append(entities, ref.EntityID)
	}
	return "ce.target_ref = $1 and (ce.account_id, ce.entity_id) in (" +
			"select r.account_id, r.entity_id from unnest($2::text[], $3::text[]) as r(account_id, entity_id))",
		[]any{refName, accounts, entities}
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
