package postgres

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"

	"gitslice.io/gitslice/internal/storage"
)

// BlameStore keeps blame snapshots (design/27_blame.md).
type BlameStore struct {
	db *sql.DB
}

// blameSnapshotData is a snapshot as stored: owners as runs of lines, each
// naming a commit by its index in Commits.
type blameSnapshotData struct {
	ContentHash string   `json:"h"`
	Truncated   bool     `json:"t,omitempty"`
	Lines       int      `json:"n"`
	Commits     []string `json:"c"`
	// Runs are [line count, commit index] pairs, in line order.
	Runs [][2]int `json:"r"`
}

func encodeBlameSnapshot(snapshot storage.BlameSnapshot) ([]byte, error) {
	data := blameSnapshotData{ContentHash: snapshot.ContentHash, Truncated: snapshot.Truncated, Lines: len(snapshot.Owners)}
	index := map[string]int{}
	for i := 0; i < len(snapshot.Owners); {
		owner := snapshot.Owners[i]
		j := i
		for j < len(snapshot.Owners) && snapshot.Owners[j] == owner {
			j++
		}
		k, ok := index[owner]
		if !ok {
			k = len(data.Commits)
			index[owner] = k
			data.Commits = append(data.Commits, owner)
		}
		data.Runs = append(data.Runs, [2]int{j - i, k})
		i = j
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeBlameSnapshot(commitID, p string, stored []byte) (*storage.BlameSnapshot, error) {
	zr, err := gzip.NewReader(bytes.NewReader(stored))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var data blameSnapshotData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	owners := make([]string, 0, data.Lines)
	for _, run := range data.Runs {
		if run[1] < 0 || run[1] >= len(data.Commits) {
			return nil, fmt.Errorf("blame snapshot %s %s: bad commit index", commitID, p)
		}
		for i := 0; i < run[0]; i++ {
			owners = append(owners, data.Commits[run[1]])
		}
	}
	if len(owners) != data.Lines {
		return nil, fmt.Errorf("blame snapshot %s %s: %d lines, want %d", commitID, p, len(owners), data.Lines)
	}
	return &storage.BlameSnapshot{CommitID: commitID, Path: p, ContentHash: data.ContentHash, Truncated: data.Truncated, Owners: owners}, nil
}

func (s *BlameStore) BlameSnapshots(ctx context.Context, commitIDs []string) (map[string]map[string]*storage.BlameSnapshot, error) {
	out := map[string]map[string]*storage.BlameSnapshot{}
	if len(commitIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `select commit_id, path, data from blame_snapshots where commit_id = any($1)`, commitIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var commitID, p string
		var data []byte
		if err := rows.Scan(&commitID, &p, &data); err != nil {
			return nil, err
		}
		snapshot, err := decodeBlameSnapshot(commitID, p, data)
		if err != nil {
			// A snapshot that cannot be read is recomputed, not an error.
			continue
		}
		if out[commitID] == nil {
			out[commitID] = map[string]*storage.BlameSnapshot{}
		}
		out[commitID][p] = snapshot
	}
	return out, rows.Err()
}

func (s *BlameStore) PutBlameSnapshot(ctx context.Context, snapshot storage.BlameSnapshot) error {
	data, err := encodeBlameSnapshot(snapshot)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		insert into blame_snapshots(commit_id, path, data) values ($1, $2, $3)
		on conflict (commit_id, path) do nothing
	`, snapshot.CommitID, snapshot.Path, data)
	return err
}
