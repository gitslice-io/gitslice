package gitcompat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

var errInvalidUploadRequest = errors.New("invalid git upload-pack request")

// Hydration and the lazy build's file reads write hydrateShards packs at once. A
// pack is closed at hydratePackBytes or hydratePackObjects. A hydration of at
// least hydrateLogThreshold files is logged.
const (
	hydrateShards       = 6
	hydratePackBytes    = 512 << 20
	hydratePackObjects  = 50_000
	hydrateLogThreshold = 200
)

// HydrateFor makes sure the repository holds the file contents a git-upload-pack
// request would send. On a lazily built projection most files are absent, so
// this runs before git answers: it works out which contents the fetch needs,
// reads them from the object store, and writes them into the repository. A
// blobless clone needs none; a full clone needs all of them, once.
func (p *Projector) HydrateFor(ctx context.Context, repoPath string, body []byte) error {
	if p.blobs == nil {
		return nil
	}
	req, understood := parseUploadRequest(body)
	if !understood {
		return errInvalidUploadRequest
	}
	if !req.fetch {
		return nil
	}
	unlock := p.lockHydrate(repoPath)
	defer unlock()
	// Walking the history to find what is missing costs time on a large
	// repository, so once everything at a head is present, that is remembered
	// until the head moves.
	head, _ := gitOutput(ctx, repoPath, nil, "rev-parse", "--verify", "-q", projectedBranch)
	head = strings.TrimSpace(head)
	if head != "" && p.completeAt(repoPath) == head {
		return nil
	}
	wantsHead := head != "" && (slices.Contains(req.wants, head) || slices.Contains(req.wantRefs, projectedBranch))
	// Everything reachable from the last complete head is present, so only what
	// is newer needs searching.
	if wantsHead && !req.excludesBlobs() && len(req.haves) == 0 {
		// A clone of whole files: worth a cover (cover.go).
		p.noteFullFetch(repoPath)
	}
	missing, err := p.missingContents(ctx, repoPath, req, p.completeAt(repoPath))
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		if err := p.hydrate(ctx, repoPath, missing); err != nil {
			return err
		}
	}
	// A request for the whole history at head, with nothing assumed on the
	// client's side, leaves nothing missing at this head.
	if wantsHead && !req.excludesBlobs() && req.depth == 0 && len(req.haves) == 0 {
		p.setComplete(repoPath, head)
	}
	return nil
}

func (p *Projector) completeAt(repoPath string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.complete[repoPath]
}

func (p *Projector) setComplete(repoPath, head string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if head == "" {
		delete(p.complete, repoPath)
		return
	}
	if p.complete == nil {
		p.complete = map[string]string{}
	}
	if _, exists := p.complete[repoPath]; !exists && len(p.complete) >= maxTrackedRepos {
		for k := range p.complete {
			delete(p.complete, k)
			break
		}
	}
	p.complete[repoPath] = head
}

func (p *Projector) lockHydrate(repoPath string) func() {
	return p.acquireLock(repoPath + "#hydrate")
}

// missingContents returns the reachable Git blob ids the request needs that the
// repository does not have. Foreign or unreachable SHA-1s are never hydrated.
func (p *Projector) missingContents(ctx context.Context, repoPath string, req uploadRequest, completeHead string) ([]string, error) {
	wantOIDs := append([]string(nil), req.wants...)
	if len(req.wantRefs) > 0 {
		resolved, err := resolveRepoRefs(ctx, repoPath, req.wantRefs)
		if err != nil {
			return nil, err
		}
		wantOIDs = append(wantOIDs, resolved...)
	}
	checkOIDs := make([]string, 0, len(wantOIDs)+len(req.haves)+1)
	checkOIDs = append(checkOIDs, wantOIDs...)
	checkOIDs = append(checkOIDs, req.haves...)
	if completeHead != "" {
		checkOIDs = append(checkOIDs, completeHead)
	}
	objectTypes, err := batchObjectTypes(ctx, repoPath, checkOIDs)
	if err != nil {
		return nil, err
	}

	missing := map[string]bool{}
	var walkWants []string
	var directMissingWants []string
	seenWant := map[string]struct{}{}
	for _, want := range wantOIDs {
		if _, dup := seenWant[want]; dup {
			continue
		}
		seenWant[want] = struct{}{}
		if typ, ok := objectTypes[want]; ok {
			if typ != "blob" {
				walkWants = append(walkWants, want)
			}
			continue
		}
		directMissingWants = append(directMissingWants, want)
	}

	// Partial-clone clients ask for missing blobs directly by SHA-1. Only
	// hydrate direct blob wants that are reachable from this repository's own
	// projected refs so another tenant's blob cannot be pulled into repoPath.
	if len(directMissingWants) > 0 {
		unresolved := directMissingWants
		if headMissing, err := reachableMissingBlobs(ctx, repoPath, "--no-walk", projectedBranch); err == nil {
			var rest []string
			for _, oid := range unresolved {
				if headMissing[oid] {
					missing[oid] = true
				} else {
					rest = append(rest, oid)
				}
			}
			unresolved = rest
		}
		if len(unresolved) > 0 {
			args := []string{"--branches", "--tags"}
			if completeHead != "" && objectTypes[completeHead] == "commit" {
				args = append(args, "--not", completeHead)
			}
			reachableMissing, err := reachableMissingBlobs(ctx, repoPath, args...)
			if err != nil {
				return nil, err
			}
			for _, oid := range unresolved {
				if reachableMissing[oid] {
					missing[oid] = true
				}
			}
		}
	}

	if !req.excludesBlobs() && len(walkWants) > 0 {
		var stdin strings.Builder
		for _, w := range walkWants {
			stdin.WriteString(w)
			stdin.WriteByte('\n')
		}
		if req.depth == 0 {
			for _, h := range req.haves {
				if typ := objectTypes[h]; typ == "commit" || typ == "tag" {
					stdin.WriteByte('^')
					stdin.WriteString(h)
					stdin.WriteByte('\n')
				}
			}
			if completeHead != "" && objectTypes[completeHead] == "commit" {
				stdin.WriteByte('^')
				stdin.WriteString(completeHead)
				stdin.WriteByte('\n')
			}
		}
		args := []string{"rev-list", "--objects", "--missing=print"}
		if req.depth > 0 {
			args = append(args, "--max-count="+strconv.Itoa(req.depth))
		}
		args = append(args, "--stdin")
		out, err := gitOutputInput(ctx, repoPath, []string{"GIT_DIR=" + repoPath}, []byte(stdin.String()), args...)
		if err != nil {
			return nil, err
		}
		addMissingLines(missing, out)
	}

	ids := make([]string, 0, len(missing))
	for id := range missing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func resolveRepoRefs(ctx context.Context, repoPath string, refs []string) ([]string, error) {
	args := append([]string{"for-each-ref", "--format=%(objectname)", "--"}, refs...)
	out, err := gitOutput(ctx, repoPath, []string{"GIT_DIR=" + repoPath}, args...)
	if err != nil {
		return nil, err
	}
	var oids []string
	for _, line := range strings.Split(out, "\n") {
		if oid := strings.TrimSpace(line); validOID(oid) {
			oids = append(oids, oid)
		}
	}
	return oids, nil
}

// batchObjectTypes inspects every OID in one git cat-file --batch-check call
// and returns the object type ("commit", "tree", "blob", "tag") for OIDs
// present in repoPath.
func batchObjectTypes(ctx context.Context, repoPath string, oids []string) (map[string]string, error) {
	types := map[string]string{}
	if len(oids) == 0 {
		return types, nil
	}
	var stdin strings.Builder
	seen := make(map[string]struct{}, len(oids))
	for _, oid := range oids {
		if !validOID(oid) {
			continue
		}
		if _, dup := seen[oid]; dup {
			continue
		}
		seen[oid] = struct{}{}
		stdin.WriteString(oid)
		stdin.WriteByte('\n')
	}
	if stdin.Len() == 0 {
		return types, nil
	}
	out, err := gitOutputInput(ctx, repoPath, []string{"GIT_DIR=" + repoPath}, []byte(stdin.String()), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] != "missing" {
			types[fields[0]] = fields[1]
		}
	}
	return types, nil
}

func reachableMissingBlobs(ctx context.Context, repoPath string, revArgs ...string) (map[string]bool, error) {
	args := append([]string{"rev-list", "--objects", "--missing=print"}, revArgs...)
	out, err := gitOutput(ctx, repoPath, []string{"GIT_DIR=" + repoPath}, args...)
	if err != nil {
		return nil, err
	}
	missing := map[string]bool{}
	addMissingLines(missing, out)
	return missing, nil
}

func addMissingLines(dst map[string]bool, out string) {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "?") {
			if oid := strings.TrimSpace(line[1:]); validOID(oid) {
				dst[oid] = true
			}
		}
	}
}

// hydrate writes the file contents with the given Git blob ids into the
// repository. Each is checked against its id as it is written.
func (p *Projector) hydrate(ctx context.Context, repoPath string, ids []string) error {
	started := time.Now()
	records, err := p.blobs.BlobsByGitIDs(ctx, ids)
	if err != nil {
		return err
	}
	var unknown []string
	for _, id := range ids {
		if records[id] == nil {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%d files the repository needs have no record (first: %s); the Git blob id backfill has not reached them", len(unknown), unknown[0])
	}
	shards := make([][]string, min(hydrateShards, len(ids)))
	for i, id := range ids {
		shards[i%len(shards)] = append(shards[i%len(shards)], id)
	}
	errs := make(chan error, len(shards))
	var total int64
	var mu sync.Mutex
	for _, shard := range shards {
		go func() {
			n, err := p.hydrateShard(ctx, repoPath, shard, records)
			mu.Lock()
			total += n
			mu.Unlock()
			errs <- err
		}()
	}
	var first error
	for range shards {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		return first
	}
	if len(ids) >= hydrateLogThreshold {
		slog.Info("git contents hydrated", "repo", filepath.Base(filepath.Dir(repoPath))+"/"+filepath.Base(repoPath), "files", len(ids), "bytes", total, "total_ms", time.Since(started).Milliseconds())
	}
	return nil
}

func (p *Projector) hydrateShard(ctx context.Context, repoPath string, ids []string, records map[string]*corev1.BlobRecord) (int64, error) {
	var written int64
	pack, err := newPackBuilder(repoPath)
	if err != nil {
		return 0, err
	}
	flush := func() error {
		_, err := pack.finish(ctx, repoPath)
		pack = nil
		return err
	}
	for _, id := range ids {
		if pack == nil {
			if pack, err = newPackBuilder(repoPath); err != nil {
				return written, err
			}
		}
		record := records[id]
		rc, err := p.objectStore.Get(ctx, filesystem.BlobKey(record.ContentHash), 0, 0)
		if err != nil {
			pack.discard()
			return written, fmt.Errorf("read blob %s: %w", record.ContentHash, err)
		}
		got, err := pack.addBlob(record.Size, rc)
		_ = rc.Close()
		if err != nil {
			pack.discard()
			return written, fmt.Errorf("hydrate %s: %w", id, err)
		}
		if got != id {
			pack.discard()
			return written, fmt.Errorf("blob %s hashes to %s, not the recorded %s", record.ContentHash, got, id)
		}
		written += record.Size
		if pack.Size() >= hydratePackBytes || pack.Objects() >= hydratePackObjects {
			if err := flush(); err != nil {
				return written, err
			}
		}
	}
	if pack != nil {
		if err := flush(); err != nil {
			return written, err
		}
	}
	return written, nil
}
