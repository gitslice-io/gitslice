package blame

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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

func makeSteps(versions [][]string, commits []string, from int) []Step {
	var steps []Step
	for v := len(versions) - 1; v >= from; v-- {
		step := Step{Commit: &corev1.Commit{Id: commits[v]}, Version: Version{Hash: fmt.Sprint(v), Lines: versions[v]}}
		if v > 0 {
			step.Parent = &Version{Hash: fmt.Sprint(v - 1), Lines: versions[v-1]}
		}
		steps = append(steps, step)
	}
	return steps
}

func carryAll(owners []string, steps []Step) []string {
	for i := len(steps) - 1; i >= 0; i-- {
		owners = Carry(owners, steps[i])
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
		if got := carryAll(nil, makeSteps(versions, commits, 0)); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("round %d from creation:\n got %v\nwant %v", round, got, want)
		}
		for snap := 0; snap < len(versions)-1; snap++ {
			base := backwardBlame(versions[:snap+1], commits[:snap+1])
			if got := carryAll(base, makeSteps(versions, commits, snap+1)); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("round %d from a snapshot of version %d:\n got %v\nwant %v", round, snap, got, want)
			}
		}
	}
}

type pagedTestRepo struct {
	storage.RepositoryStore
	commits []*corev1.Commit
}

func (r *pagedTestRepo) ListCommitPageByPathPrefixes(_ context.Context, _ string, _ []string, limit int, pageToken string) (*storage.CommitListPage, error) {
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

func TestWalkStopsPagePrefetchAndBlobsBetweenPages(t *testing.T) {
	mem := memory.New()
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

	engine := &Engine{
		Repository: &pagedTestRepo{RepositoryStore: mem.Repository, commits: newestFirst},
		Blames:     mem.Blames,
		Objects:    mem.Objects,
	}

	headEntry, found, err := engine.Entry(ctx, "c4", filePath)
	if err != nil || !found {
		t.Fatalf("Entry(c4) = (%v, %v, %v)", headEntry, found, err)
	}

	type pagePair struct {
		blobsCtx    context.Context
		prefetchCtx context.Context
	}
	var pages []pagePair
	state := &WalkState{
		RefName:      storage.DefaultTargetRef,
		Filters:      CommitFilters{Prefixes: []string{filePath}},
		HeadCommitID: "c4",
		HeadPath:     filePath,
		HeadEntry:    headEntry,
		PageSize:     2,
		OnPageContexts: func(blobsCtx, prefetchCtx context.Context) {
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

	res, err := engine.WalkWithState(ctx, state)
	if err != nil {
		t.Fatalf("WalkWithState() error = %v", err)
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
	if fmt.Sprint(res.Owners) != fmt.Sprint(wantOwners) {
		t.Fatalf("owners = %v, want %v", res.Owners, wantOwners)
	}
}

type countingRefRepo struct {
	storage.RepositoryStore
	calls atomic.Int64
}

func (r *countingRefRepo) GetRef(ctx context.Context, name string) (*corev1.Ref, error) {
	r.calls.Add(1)
	return r.RepositoryStore.GetRef(ctx, name)
}

func TestWarmerRespectsInjectedActivityGate(t *testing.T) {
	mem := memory.New()
	repo := &countingRefRepo{RepositoryStore: mem.Repository}
	gate := activity.NewIdle(time.Minute)
	warmer := NewWarmer(&Engine{Repository: repo, Blames: mem.Blames, Objects: mem.Objects}, gate, nil)
	warmer.Interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		warmer.Run(ctx)
	}()

	time.Sleep(40 * time.Millisecond)
	if got := repo.calls.Load(); got != 0 {
		cancel()
		<-done
		t.Fatalf("GetRef called %d times while injected gate was idle, want 0", got)
	}

	gate.Begin()()
	deadline := time.Now().Add(2 * time.Second)
	for repo.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if repo.calls.Load() == 0 {
		t.Fatal("Warmer.Run did not poll after injected gate woke")
	}
}
