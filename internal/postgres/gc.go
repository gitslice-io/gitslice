package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

const defaultGCSampleLimit = 100

// GCReport summarizes objects that are unreachable from live roots.
// It is advisory only; nothing is deleted.
type GCReport struct {
	Roots                      GCRoots  `json:"roots"`
	BlobCount                  int      `json:"blob_count"`
	ReachableBlobCount         int      `json:"reachable_blob_count"`
	ReachableTreeCount         int      `json:"reachable_tree_count"`
	OrphanBlobs                []string `json:"orphan_blobs"`
	OrphanBlobCount            int      `json:"orphan_blob_count"`
	OrphanTreeNodes            []string `json:"orphan_tree_nodes"`
	OrphanTreeCount            int      `json:"orphan_tree_count"`
	TreeNodeEnumerationLimited bool     `json:"tree_node_enumeration_limited"`
	AbandonedPatchsets         []string `json:"abandoned_patchsets"`
	AbandonedPatchsetCount     int      `json:"abandoned_patchset_count"`
	Notes                      []string `json:"notes,omitempty"`
}

type GCRoots struct {
	Refs           int `json:"refs"`
	LiveChangesets int `json:"live_changesets"`
	PendingPublish int `json:"pending_publish"`
}

type GCOptions struct {
	SampleLimit int
}

type gcReachability struct {
	blobIDs            map[string]bool
	treeIDs            map[string]bool
	commitIDs          map[string]bool
	blobIDsByContentID map[string][]string
}

type gcPatchsetRoot struct {
	ID           string
	ChangesetID  string
	BaseCommitID string
	BaseTreeID   string
	ResultTreeID string
	FileEdits    []*corev1.FileEdit
	PathBases    []*corev1.PathBase
	Conflicts    []*corev1.PatchsetConflict
}

// ReportUnreachable enumerates reachability roots and returns objects that are
// not reachable from any of them. It performs NO deletion.
//
// Tree-node orphan reporting is currently limited because ObjectReader only
// supports point reads and does not expose object-store key enumeration. The
// report still tracks reachable tree IDs to protect blobs reached through live
// roots, but leaves orphan tree samples/counts empty with a limitation note.
func (d *DB) ReportUnreachable(ctx context.Context, objects ObjectReader, opts GCOptions) (GCReport, error) {
	sampleLimit := normalizedGCSampleLimit(opts.SampleLimit)
	report := GCReport{
		TreeNodeEnumerationLimited: true,
		Notes: []string{
			"orphan tree-node reporting is limited: ObjectReader supports reads but not object-store key enumeration",
		},
	}
	if objects == nil {
		report.Notes = append(report.Notes, "object reader was not supplied")
	}

	blobs, err := d.loadIntegrityBlobs(ctx)
	if err != nil {
		return report, err
	}
	report.BlobCount = len(blobs)
	reachable := newGCReachability(blobs)

	refCount, err := d.markGCRefRoots(ctx, reachable)
	if err != nil {
		return report, err
	}
	report.Roots.Refs = refCount

	liveChangesets, err := d.markGCLiveChangesetRoots(ctx, reachable)
	if err != nil {
		return report, err
	}
	report.Roots.LiveChangesets = liveChangesets

	pendingPublish, err := d.markGCPendingPublishRoots(ctx, reachable)
	if err != nil {
		return report, err
	}
	report.Roots.PendingPublish = pendingPublish

	report.ReachableBlobCount = len(reachable.blobIDs)
	report.ReachableTreeCount = len(reachable.treeIDs)
	report.OrphanBlobs, report.OrphanBlobCount = gcOrphanBlobSamples(blobs, reachable.blobIDs, sampleLimit)

	abandoned, abandonedCount, err := d.loadGCAbandonedPatchsets(ctx, sampleLimit)
	if err != nil {
		return report, err
	}
	report.AbandonedPatchsets = abandoned
	report.AbandonedPatchsetCount = abandonedCount

	return report, nil
}

func normalizedGCSampleLimit(limit int) int {
	if limit <= 0 {
		return defaultGCSampleLimit
	}
	return limit
}

func newGCReachability(blobs map[string]integrityBlob) *gcReachability {
	byContent := make(map[string][]string, len(blobs))
	for id, blob := range blobs {
		if strings.TrimSpace(blob.ContentHash) == "" {
			continue
		}
		byContent[blob.ContentHash] = append(byContent[blob.ContentHash], id)
	}
	return &gcReachability{
		blobIDs:            map[string]bool{},
		treeIDs:            map[string]bool{},
		commitIDs:          map[string]bool{},
		blobIDsByContentID: byContent,
	}
}

func (r *gcReachability) markBlobID(blobID string) {
	blobID = strings.TrimSpace(blobID)
	if blobID == "" {
		return
	}
	r.blobIDs[blobID] = true
}

func (r *gcReachability) markContentHash(contentHash string) {
	contentHash = strings.TrimSpace(contentHash)
	if contentHash == "" {
		return
	}
	for _, blobID := range r.blobIDsByContentID[contentHash] {
		r.markBlobID(blobID)
	}
}

func (d *DB) markGCRefRoots(ctx context.Context, reachable *gcReachability) (int, error) {
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
		if err := d.markGCCommitRoot(ctx, reachable, commitID); err != nil {
			return 0, fmt.Errorf("ref %s commit %s: %w", name, commitID, err)
		}
	}
	return count, rows.Err()
}

func (d *DB) markGCLiveChangesetRoots(ctx context.Context, reachable *gcReachability) (int, error) {
	rows, err := d.db.QueryContext(ctx, `
		select id, base_commit_id, coalesce(commit_id, '')
		from changesets
		where lower(status) not in ('abandoned', 'discarded', 'terminal_discarded', 'terminal-discarded')
		order by created_at, id
	`)
	if err != nil {
		return 0, err
	}
	count := 0
	for rows.Next() {
		var changesetID, baseCommitID, commitID string
		if err := rows.Scan(&changesetID, &baseCommitID, &commitID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		count++
		if err := d.markGCCommitRoot(ctx, reachable, baseCommitID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("changeset %s base commit %s: %w", changesetID, baseCommitID, err)
		}
		if err := d.markGCCommitRoot(ctx, reachable, commitID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("changeset %s commit %s: %w", changesetID, commitID, err)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	return count, d.markGCPatchsetRoots(ctx, reachable, `
		select p.id, p.changeset_id, p.base_commit_id, p.file_edits, p.path_bases,
		       p.conflicts, p.base_tree_id, p.result_tree_id
		from patchsets p
		join changesets c on c.id = p.changeset_id
		where lower(c.status) not in ('abandoned', 'discarded', 'terminal_discarded', 'terminal-discarded')
		order by c.created_at, c.id, p.number, p.id
	`)
}

func (d *DB) markGCPendingPublishRoots(ctx context.Context, reachable *gcReachability) (int, error) {
	rows, err := d.db.QueryContext(ctx, `
		select id, changeset_id, patchset_id, base_ref_commit_id, coalesce(commit_id, '')
		from pending_publish
		order by sequence, id
	`)
	if err != nil {
		return 0, err
	}
	count := 0
	for rows.Next() {
		var pendingID, changesetID, patchsetID, baseRefCommitID, commitID string
		if err := rows.Scan(&pendingID, &changesetID, &patchsetID, &baseRefCommitID, &commitID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		count++
		if err := d.markGCCommitRoot(ctx, reachable, baseRefCommitID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("pending publish %s base ref commit %s: %w", pendingID, baseRefCommitID, err)
		}
		if err := d.markGCCommitRoot(ctx, reachable, commitID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("pending publish %s commit %s: %w", pendingID, commitID, err)
		}
		_ = changesetID
		_ = patchsetID
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	if err := d.markGCPatchsetRoots(ctx, reachable, `
		select distinct p.id, p.changeset_id, p.base_commit_id, p.file_edits, p.path_bases,
		       p.conflicts, p.base_tree_id, p.result_tree_id
		from patchsets p
		join pending_publish pending on pending.changeset_id = p.changeset_id
		order by p.changeset_id, p.id
	`); err != nil {
		return 0, err
	}
	return count, nil
}

func (d *DB) markGCPatchsetRoots(ctx context.Context, reachable *gcReachability, query string, args ...any) error {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		patchset, err := scanGCPatchsetRoot(rows)
		if err != nil {
			return err
		}
		if err := d.markGCPatchsetRoot(ctx, reachable, patchset); err != nil {
			return err
		}
	}
	return rows.Err()
}

func scanGCPatchsetRoot(row scanner) (gcPatchsetRoot, error) {
	var patchset gcPatchsetRoot
	var fileEditsJSON, pathBasesJSON, conflictsJSON []byte
	var baseTreeID, resultTreeID sql.NullString
	err := row.Scan(&patchset.ID, &patchset.ChangesetID, &patchset.BaseCommitID,
		&fileEditsJSON, &pathBasesJSON, &conflictsJSON, &baseTreeID, &resultTreeID)
	if err != nil {
		return gcPatchsetRoot{}, err
	}
	for _, item := range []struct {
		raw []byte
		dst any
	}{
		{fileEditsJSON, &patchset.FileEdits},
		{pathBasesJSON, &patchset.PathBases},
		{conflictsJSON, &patchset.Conflicts},
	} {
		if err := decodeJSON(item.raw, item.dst); err != nil {
			return gcPatchsetRoot{}, err
		}
	}
	if baseTreeID.Valid {
		patchset.BaseTreeID = baseTreeID.String
	}
	if resultTreeID.Valid {
		patchset.ResultTreeID = resultTreeID.String
	}
	return patchset, nil
}

func (d *DB) markGCPatchsetRoot(ctx context.Context, reachable *gcReachability, patchset gcPatchsetRoot) error {
	if err := d.markGCCommitRoot(ctx, reachable, patchset.BaseCommitID); err != nil {
		return fmt.Errorf("patchset %s base commit %s: %w", patchset.ID, patchset.BaseCommitID, err)
	}
	if err := d.markGCTreeRoot(ctx, reachable, patchset.BaseTreeID); err != nil {
		return fmt.Errorf("patchset %s base tree %s: %w", patchset.ID, patchset.BaseTreeID, err)
	}
	if err := d.markGCTreeRoot(ctx, reachable, patchset.ResultTreeID); err != nil {
		return fmt.Errorf("patchset %s result tree %s: %w", patchset.ID, patchset.ResultTreeID, err)
	}
	for _, edit := range patchset.FileEdits {
		if edit == nil {
			continue
		}
		reachable.markBlobID(edit.BlobId)
		reachable.markContentHash(edit.ContentHash)
	}
	for _, base := range patchset.PathBases {
		if base == nil {
			continue
		}
		reachable.markBlobID(base.BlobId)
		reachable.markContentHash(base.ContentHash)
		if err := d.markGCTreeRoot(ctx, reachable, base.TreeId); err != nil {
			return fmt.Errorf("patchset %s path base tree %s: %w", patchset.ID, base.TreeId, err)
		}
	}
	for _, conflict := range patchset.Conflicts {
		if conflict == nil {
			continue
		}
		reachable.markContentHash(conflict.BaseContentHash)
		reachable.markContentHash(conflict.LocalContentHash)
		reachable.markContentHash(conflict.RemoteContentHash)
	}
	return nil
}

func (d *DB) markGCCommitRoot(ctx context.Context, reachable *gcReachability, commitID string) error {
	commitID = strings.TrimSpace(commitID)
	if commitID == "" || reachable.commitIDs[commitID] {
		return nil
	}
	reachable.commitIDs[commitID] = true
	rootTreeID, err := d.repository.RootTreeForCommit(ctx, commitID)
	if err != nil {
		return err
	}
	return d.markGCTreeRoot(ctx, reachable, rootTreeID)
}

func (d *DB) markGCTreeRoot(ctx context.Context, reachable *gcReachability, treeID string) error {
	treeID = strings.TrimSpace(treeID)
	if treeID == "" || reachable.treeIDs[treeID] {
		return nil
	}
	reachable.treeIDs[treeID] = true
	return d.collectGCTreeEntries(ctx, treeID, "/", reachable)
}

func (d *DB) collectGCTreeEntries(ctx context.Context, rootTreeID, p string, reachable *gcReachability) error {
	entries, err := d.repository.ListDirectoryAtTree(ctx, rootTreeID, p)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Kind {
		case "file":
			reachable.markBlobID(entry.BlobID)
			reachable.markContentHash(entry.ContentHash)
		case "directory":
			childTreeID := strings.TrimSpace(entry.TreeID)
			if childTreeID == "" || reachable.treeIDs[childTreeID] {
				continue
			}
			reachable.treeIDs[childTreeID] = true
			if err := d.collectGCTreeEntries(ctx, rootTreeID, entry.Path, reachable); err != nil {
				return err
			}
		}
	}
	return nil
}

func gcOrphanBlobSamples(blobs map[string]integrityBlob, reachable map[string]bool, sampleLimit int) ([]string, int) {
	ids := make([]string, 0, len(blobs))
	for id := range blobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var samples []string
	count := 0
	for _, id := range ids {
		if reachable[id] {
			continue
		}
		count++
		if len(samples) < sampleLimit {
			samples = append(samples, id)
		}
	}
	return samples, count
}

func (d *DB) loadGCAbandonedPatchsets(ctx context.Context, sampleLimit int) ([]string, int, error) {
	rows, err := d.db.QueryContext(ctx, `
		select p.id
		from patchsets p
		join changesets c on c.id = p.changeset_id
		where lower(c.status) in ('abandoned', 'discarded', 'terminal_discarded', 'terminal-discarded')
		order by c.updated_at, c.id, p.number, p.id
	`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var samples []string
	count := 0
	for rows.Next() {
		var patchsetID string
		if err := rows.Scan(&patchsetID); err != nil {
			return nil, 0, err
		}
		count++
		if len(samples) < sampleLimit {
			samples = append(samples, patchsetID)
		}
	}
	return samples, count, rows.Err()
}
