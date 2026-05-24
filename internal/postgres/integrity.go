package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
)

type ObjectReader interface {
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
}

type IntegrityReport struct {
	RefCount            int                `json:"ref_count"`
	CommitCount         int                `json:"commit_count"`
	UniqueRootTreeCount int                `json:"unique_root_tree_count"`
	TreeCount           int                `json:"tree_count"`
	TreeFileCount       int                `json:"tree_file_count"`
	BlobCount           int                `json:"blob_count"`
	PathHeadCount       int                `json:"path_head_count"`
	PendingPublishCount int                `json:"pending_publish_count"`
	Findings            []IntegrityFinding `json:"findings,omitempty"`
}

func (r IntegrityReport) OK() bool {
	return len(r.Findings) == 0
}

type IntegrityFinding struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type IntegrityError struct {
	Findings []IntegrityFinding
}

func (e IntegrityError) Error() string {
	if len(e.Findings) == 0 {
		return "integrity verification failed"
	}
	if len(e.Findings) == 1 {
		return "integrity verification failed: " + e.Findings[0].Code + ": " + e.Findings[0].Detail
	}
	return fmt.Sprintf("integrity verification failed with %d findings: %s: %s", len(e.Findings), e.Findings[0].Code, e.Findings[0].Detail)
}

type integrityBlob struct {
	ID              string
	ContentHash     string
	Size            int64
	StorageLocation string
	State           string
}

type integrityCommit struct {
	ID           string
	ParentIDs    []string
	RootTreeID   string
	Author       string
	Message      string
	CreatedAt    time.Time
	ChangedPaths []string
}

func (d *DB) VerifyIntegrity(ctx context.Context, objects ObjectReader) (IntegrityReport, error) {
	var report IntegrityReport
	addFinding := func(code, detail string) {
		report.Findings = append(report.Findings, IntegrityFinding{Code: code, Detail: detail})
	}

	blobs, err := d.loadIntegrityBlobs(ctx)
	if err != nil {
		return report, err
	}
	report.BlobCount = len(blobs)
	if objects == nil && len(blobs) > 0 {
		addFinding("object_reader_missing", "blob rows exist but no filesystem object reader was supplied")
	}
	for _, blob := range blobs {
		if blob.State != "available" {
			addFinding("blob_unavailable", fmt.Sprintf("blob %s has state %q", blob.ID, blob.State))
			continue
		}
		if objects != nil {
			verifyBlobObject(ctx, objects, blob, addFinding)
		}
	}

	commits, err := d.loadIntegrityCommits(ctx)
	if err != nil {
		return report, err
	}
	report.CommitCount = len(commits)
	commitByID := make(map[string]integrityCommit, len(commits))
	for _, commit := range commits {
		commitByID[commit.ID] = commit
		computed := objectid.CommitID(objectid.CommitObject{
			ParentIDs:    commit.ParentIDs,
			RootTreeID:   commit.RootTreeID,
			Author:       commit.Author,
			Message:      commit.Message,
			CreatedAt:    commit.CreatedAt,
			ChangedPaths: commit.ChangedPaths,
		})
		if computed != commit.ID {
			addFinding("commit_id_mismatch", fmt.Sprintf("commit %s computed as %s", commit.ID, computed))
		}
	}
	for _, commit := range commits {
		for _, parentID := range commit.ParentIDs {
			if _, ok := commitByID[parentID]; !ok {
				addFinding("missing_commit_parent", fmt.Sprintf("commit %s parent %s is missing", commit.ID, parentID))
			}
		}
	}

	refCount, err := d.verifyRefs(ctx, commitByID, addFinding)
	if err != nil {
		return report, err
	}
	report.RefCount = refCount

	if d.repository.trees == nil {
		addFinding("tree_store_missing", "tree store is not configured")
	} else {
		verifiedRoots := map[string]bool{}
		for _, commit := range commits {
			if verifiedRoots[commit.RootTreeID] {
				continue
			}
			verifiedRoots[commit.RootTreeID] = true
			treeReport, err := d.repository.trees.VerifyReachable(ctx, commit.RootTreeID)
			if err != nil {
				addFinding("tree_unreadable", fmt.Sprintf("commit %s root %s: %v", commit.ID, commit.RootTreeID, err))
				continue
			}
			report.UniqueRootTreeCount++
			report.TreeCount += treeReport.TreeCount
			report.TreeFileCount += treeReport.FileCount
			verifyTreeBlobReferences(ctx, d, commit.RootTreeID, blobs, addFinding)
		}
	}

	pendingCount, err := d.pendingPublishCount(ctx)
	if err != nil {
		return report, err
	}
	report.PendingPublishCount = pendingCount
	if pendingCount == 0 {
		pathHeadCount, err := d.verifyPathHeadsAgainstCurrentRef(ctx, addFinding)
		if err != nil {
			return report, err
		}
		report.PathHeadCount = pathHeadCount
	}

	if !report.OK() {
		return report, IntegrityError{Findings: report.Findings}
	}
	return report, nil
}

func (d *DB) loadIntegrityBlobs(ctx context.Context) (map[string]integrityBlob, error) {
	rows, err := d.db.QueryContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		order by id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]integrityBlob{}
	for rows.Next() {
		var blob integrityBlob
		if err := rows.Scan(&blob.ID, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State); err != nil {
			return nil, err
		}
		out[blob.ID] = blob
	}
	return out, rows.Err()
}

func (d *DB) loadIntegrityCommits(ctx context.Context) ([]integrityCommit, error) {
	rows, err := d.db.QueryContext(ctx, `
		select id, parent_ids, root_tree_id, coalesce(author_subject_id, ''),
		       message, created_at, changed_paths
		from commits
		order by created_at, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []integrityCommit
	for rows.Next() {
		var commit integrityCommit
		var parentJSON, changedJSON []byte
		if err := rows.Scan(&commit.ID, &parentJSON, &commit.RootTreeID, &commit.Author, &commit.Message, &commit.CreatedAt, &changedJSON); err != nil {
			return nil, err
		}
		if err := decodeJSON(parentJSON, &commit.ParentIDs); err != nil {
			return nil, err
		}
		if err := decodeJSON(changedJSON, &commit.ChangedPaths); err != nil {
			return nil, err
		}
		out = append(out, commit)
	}
	return out, rows.Err()
}

func (d *DB) verifyRefs(ctx context.Context, commitByID map[string]integrityCommit, addFinding func(string, string)) (int, error) {
	rows, err := d.db.QueryContext(ctx, `select name, commit_id from refs order by name`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name, commitID string
		if err := rows.Scan(&name, &commitID); err != nil {
			return 0, err
		}
		count++
		if _, ok := commitByID[commitID]; !ok {
			addFinding("ref_missing_commit", fmt.Sprintf("ref %s points to missing commit %s", name, commitID))
		}
	}
	return count, rows.Err()
}

func verifyBlobObject(ctx context.Context, objects ObjectReader, blob integrityBlob, addFinding func(string, string)) {
	rc, err := objects.Get(ctx, blob.StorageLocation, 0, 0)
	if err != nil {
		addFinding("blob_object_unreadable", fmt.Sprintf("blob %s at %s: %v", blob.ID, blob.StorageLocation, err))
		return
	}
	defer rc.Close()
	contentHash := sha256.New()
	blobHash := sha256.New()
	blobHash.Write([]byte("gitslice.blob.v1"))
	blobHash.Write([]byte{0})
	size, err := io.Copy(io.MultiWriter(contentHash, blobHash), rc)
	if err != nil {
		addFinding("blob_object_read_failed", fmt.Sprintf("blob %s at %s: %v", blob.ID, blob.StorageLocation, err))
		return
	}
	computedContentHash := "sha256:" + hex.EncodeToString(contentHash.Sum(nil))
	computedBlobID := "sha256:" + hex.EncodeToString(blobHash.Sum(nil))
	if computedContentHash != blob.ContentHash {
		addFinding("blob_content_hash_mismatch", fmt.Sprintf("blob %s content hash %s computed as %s", blob.ID, blob.ContentHash, computedContentHash))
	}
	if computedBlobID != blob.ID {
		addFinding("blob_id_mismatch", fmt.Sprintf("blob %s computed as %s", blob.ID, computedBlobID))
	}
	if size != blob.Size {
		addFinding("blob_size_mismatch", fmt.Sprintf("blob %s size %d computed as %d", blob.ID, blob.Size, size))
	}
}

func verifyTreeBlobReferences(ctx context.Context, d *DB, rootTreeID string, blobs map[string]integrityBlob, addFinding func(string, string)) {
	files, err := d.repository.trees.ListFiles(ctx, rootTreeID, "/")
	if err != nil {
		addFinding("tree_file_list_failed", fmt.Sprintf("root %s: %v", rootTreeID, err))
		return
	}
	for _, file := range files {
		blob, ok := blobs[file.BlobID]
		if !ok {
			addFinding("tree_missing_blob", fmt.Sprintf("file %s references missing blob %s", file.Path, file.BlobID))
			continue
		}
		if blob.ContentHash != file.ContentHash {
			addFinding("tree_blob_hash_mismatch", fmt.Sprintf("file %s references blob %s with content hash %s, blob row has %s", file.Path, file.BlobID, file.ContentHash, blob.ContentHash))
		}
		if blob.Size != file.Size {
			addFinding("tree_blob_size_mismatch", fmt.Sprintf("file %s references blob %s with size %d, blob row has %d", file.Path, file.BlobID, file.Size, blob.Size))
		}
	}
}

func (d *DB) pendingPublishCount(ctx context.Context) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx, `select count(*) from pending_publish where status = 'pending'`).Scan(&count)
	return count, err
}

func (d *DB) verifyPathHeadsAgainstCurrentRef(ctx context.Context, addFinding func(string, string)) (int, error) {
	ref, err := d.repository.GetRef(ctx, DefaultTargetRef)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	files, err := d.repository.ListFiles(ctx, ref.CommitId, "/")
	if err != nil {
		return 0, err
	}
	filesByPath := make(map[string]FileEntry, len(files))
	for _, file := range files {
		filesByPath[file.Path] = file
	}

	rows, err := d.db.QueryContext(ctx, `
		select path, exists, entry_fingerprint, blob_id, content_hash, mode, size
		from path_heads
		order by path
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	heads := map[string]PathHead{}
	count := 0
	for rows.Next() {
		head, err := scanPathHead(rows)
		if err != nil {
			return 0, err
		}
		count++
		heads[head.Path] = head
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for path, file := range filesByPath {
		head, ok := heads[path]
		if !ok {
			addFinding("path_head_missing", fmt.Sprintf("current file %s has no path_head", path))
			continue
		}
		if !head.Exists {
			addFinding("path_head_false_missing", fmt.Sprintf("current file %s has a deleted path_head", path))
			continue
		}
		if got, want := head.EntryFingerprint, FileEntryFingerprint(file); got != want {
			addFinding("path_head_fingerprint_mismatch", fmt.Sprintf("path %s fingerprint %s, want %s", path, got, want))
		}
	}
	for path, head := range heads {
		if !head.Exists {
			continue
		}
		if _, ok := filesByPath[path]; !ok {
			addFinding("path_head_stale", fmt.Sprintf("path_head %s exists but current ref has no file", path))
		}
	}
	return count, nil
}

func scanPathHead(row scanner) (PathHead, error) {
	var head PathHead
	var blobID, contentHash sql.NullString
	var mode, size sql.NullInt64
	err := row.Scan(&head.Path, &head.Exists, &head.EntryFingerprint, &blobID, &contentHash, &mode, &size)
	if err != nil {
		return PathHead{}, err
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
	if head.Exists {
		if head.BlobID == "" || head.ContentHash == "" || head.Mode == 0 {
			return PathHead{}, fmt.Errorf("path_head %s exists with incomplete metadata", head.Path)
		}
	}
	if strings.TrimSpace(head.Path) == "" {
		return PathHead{}, fmt.Errorf("path_head has empty path")
	}
	return head, nil
}
