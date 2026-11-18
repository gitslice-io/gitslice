package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"testing"

	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// backwardBlame is blame computed the direct way: for each line of the
// newest version, walk back until a version does not have it.
func backwardBlame(versions [][]string, commits []string) []string {
	newest := len(versions) - 1
	owner := make([]string, len(versions[newest]))
	pos := make([]int, len(owner))
	for i := range pos {
		pos[i] = i
	}
	for v := newest; v > 0; v-- {
		match := diffutil.MatchLines(versions[v-1], versions[v], 0)
		for i := range owner {
			if owner[i] != "" {
				continue
			}
			if j := match[pos[i]]; j >= 0 {
				pos[i] = j
			} else {
				owner[i] = commits[v]
			}
		}
	}
	for i := range owner {
		if owner[i] == "" {
			owner[i] = commits[0]
		}
	}
	return owner
}

// steps builds the walk's steps for versions[from+1..]: commit v turns
// versions[v-1] into versions[v]; version 0 is created by commit 0.
func blameSteps(versions [][]string, commits []string, from int) []blameStep {
	var steps []blameStep
	for v := len(versions) - 1; v >= from; v-- {
		step := blameStep{commit: &corev1.Commit{Id: commits[v]}, version: blameVersion{hash: fmt.Sprint(v), lines: versions[v]}}
		if v > 0 {
			step.parent = &blameVersion{hash: fmt.Sprint(v - 1), lines: versions[v-1]}
		}
		steps = append(steps, step)
	}
	return steps
}

func carryAll(owners []string, steps []blameStep) []string {
	for i := len(steps) - 1; i >= 0; i-- {
		owners = carryBlame(owners, steps[i])
	}
	return owners
}

func TestCarriedBlameMatchesTheWalkFromAnySnapshot(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 200; round++ {
		lines := make([]string, 5+rng.Intn(30))
		for i := range lines {
			lines[i] = fmt.Sprintf("l%d", rng.Intn(8))
		}
		versions := [][]string{lines}
		commits := []string{"c0"}
		for v := 1; v < 2+rng.Intn(12); v++ {
			next := append([]string(nil), versions[v-1]...)
			for e := 0; e < 1+rng.Intn(4); e++ {
				switch k := rng.Intn(3); {
				case k == 0 && len(next) > 1:
					at := rng.Intn(len(next))
					next = append(next[:at], next[at+1:]...)
				case k == 1:
					at := rng.Intn(len(next) + 1)
					next = append(next[:at], append([]string{fmt.Sprintf("n%d.%d", v, e)}, next[at:]...)...)
				default:
					next[rng.Intn(len(next))] = fmt.Sprintf("e%d.%d", v, e)
				}
			}
			versions = append(versions, next)
			commits = append(commits, fmt.Sprintf("c%d", v))
		}
		want := backwardBlame(versions, commits)
		// From the creation, and from a snapshot of every older version.
		if got := carryAll(nil, blameSteps(versions, commits, 0)); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("round %d from creation:\n got %v\nwant %v", round, got, want)
		}
		for snap := 0; snap < len(versions)-1; snap++ {
			base := backwardBlame(versions[:snap+1], commits[:snap+1])
			if got := carryAll(base, blameSteps(versions, commits, snap+1)); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("round %d from a snapshot of version %d:\n got %v\nwant %v", round, snap, got, want)
			}
		}
	}
}

type pagedBlameTestRepo struct {
	storage.RepositoryStore
	commits []*corev1.Commit
}

func (r *pagedBlameTestRepo) ListCommitPageByPathPrefixes(_ context.Context, _ string, _ []string, limit int, pageToken string) (*storage.CommitListPage, error) {
	offset := 0
	if pageToken != "" {
		var err error
		offset, err = strconv.Atoi(pageToken)
		if err != nil {
			return nil, err
		}
	}
	if offset >= len(r.commits) {
		return &storage.CommitListPage{}, nil
	}
	end := min(offset+limit, len(r.commits))
	next := ""
	if end < len(r.commits) {
		next = strconv.Itoa(end)
	}
	return &storage.CommitListPage{Commits: r.commits[offset:end], NextPageToken: next}, nil
}

func TestBlameWalkStopsPagePrefetchAndBlobsBetweenPages(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := context.Background()
	const filePath = "/acme/payment/paged.txt"

	contents := [][]byte{
		[]byte("line1-c1\nline2-c1\nline3-c1\nline4-c1\n"),
		[]byte("line1-c1\nline2-c2\nline3-c1\nline4-c1\n"),
		[]byte("line1-c1\nline2-c2\nline3-c3\nline4-c1\n"),
		[]byte("line1-c1\nline2-c2\nline3-c3\nline4-c4\n"),
	}
	commitIDs := []string{"c1", "c2", "c3", "c4"}
	for i, data := range contents {
		hash := objectid.RawContentHash(data)
		mem.PutObject(filesystem.BlobKey(hash), data)
		mem.PutCommitWithFiles(commitIDs[i], []storage.FileEntry{{
			Path:        filePath,
			BlobID:      objectid.BlobID(data),
			ContentHash: hash,
			Mode:        0o100644,
			Size:        int64(len(data)),
		}}, []string{filePath})
	}
	var newestFirst []*corev1.Commit
	for i := len(commitIDs) - 1; i >= 0; i-- {
		c, err := mem.Repository.GetCommit(ctx, commitIDs[i])
		if err != nil {
			t.Fatal(err)
		}
		newestFirst = append(newestFirst, c)
	}
	handlers.Repository.Repository = &pagedBlameTestRepo{
		RepositoryStore: mem.Repository,
		commits:         newestFirst,
	}

	headEntry, found, err := handlers.Repository.blameEntry(ctx, "c4", filePath)
	if err != nil || !found {
		t.Fatalf("blameEntry(c4) = (%v, %v, %v)", headEntry, found, err)
	}

	type pagePair struct {
		blobsCtx    context.Context
		prefetchCtx context.Context
	}
	var pages []pagePair
	state := &blameWalkState{
		refName:      storage.DefaultTargetRef,
		filters:      listCommitFilters{prefixes: []string{filePath}},
		headCommitID: "c4",
		headPath:     filePath,
		headEntry:    headEntry,
		pageSize:     2,
		result:       blameResult{commits: map[string]*corev1.Commit{}},
		trackedPath:  filePath,
		trackedHash:  headEntry.ContentHash,
		first:        true,
		onPageContexts: func(blobsCtx, prefetchCtx context.Context) {
			for idx, prev := range pages {
				if !errors.Is(prev.blobsCtx.Err(), context.Canceled) {
					t.Errorf("page %d blobsCtx.Err() = %v when starting page %d, want context.Canceled", idx, prev.blobsCtx.Err(), len(pages))
				}
				if !errors.Is(prev.prefetchCtx.Err(), context.Canceled) {
					t.Errorf("page %d prefetchCtx.Err() = %v when starting page %d, want context.Canceled", idx, prev.prefetchCtx.Err(), len(pages))
				}
			}
			pages = append(pages, pagePair{blobsCtx: blobsCtx, prefetchCtx: prefetchCtx})
		},
	}

	res, err := handlers.Repository.blameWalkWithState(ctx, state)
	if err != nil {
		t.Fatalf("blameWalkWithState() error = %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("walked %d pages, want 2", len(pages))
	}
	for idx, p := range pages {
		if !errors.Is(p.blobsCtx.Err(), context.Canceled) || !errors.Is(p.prefetchCtx.Err(), context.Canceled) {
			t.Fatalf("page %d contexts not canceled after walk: blobs=%v prefetch=%v", idx, p.blobsCtx.Err(), p.prefetchCtx.Err())
		}
	}
	wantOwners := []string{"c1", "c2", "c3", "c4"}
	if fmt.Sprint(res.owners) != fmt.Sprint(wantOwners) {
		t.Fatalf("owners = %v, want %v", res.owners, wantOwners)
	}
}

func TestClaimAndMergeLocksEvictWhenIdle(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		daemonID := fmt.Sprintf("daemon-ephemeral-%d", i)
		conn := handlers.Agent.hub.registerDaemon(daemonID, daemonID+"/inst", 2)
		handlers.Agent.dispatcher.dispatchQueued(ctx, daemonID)
		handlers.Agent.hub.unregisterDaemon(conn)
	}
	if got := handlers.Agent.dispatcher.claimLocks.Len(); got != 0 {
		t.Fatalf("claimLocks.Len() = %d, want 0 when idle", got)
	}

	cs := &corev1.Changeset{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
	}
	unlock, err := handlers.Changeset.lockMergeTarget(ctx, cs)
	if err != nil {
		t.Fatalf("lockMergeTarget() error = %v", err)
	}
	if got := handlers.Changeset.changesetCore.mergeLocks.Len(); got != 1 {
		t.Fatalf("mergeLocks.Len() while held = %d, want 1", got)
	}
	unlock()
	if got := handlers.Changeset.changesetCore.mergeLocks.Len(); got != 0 {
		t.Fatalf("mergeLocks.Len() after unlock = %d, want 0", got)
	}
}
