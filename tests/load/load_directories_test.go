//go:build load

package load_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestLoadLargeDirectoryPaginationAndProjection(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, ts, conn)

	fileCount := envInt("GITSLICE_LOAD_LARGE_DIR_FILES", 1500)
	emptyDirCount := envInt("GITSLICE_LOAD_LARGE_DIR_EMPTY_DIRS", 250)
	pageSize := envInt("GITSLICE_LOAD_LARGE_DIR_PAGE_SIZE", 137)
	root := "/acme/payment/large-dir"
	sharedFile, err := loadUploadFileEdit(clients, root+"/seed.txt", []byte("large directory shared content\n"))
	if err != nil {
		t.Fatal(err)
	}
	edits := make([]*corev1.FileEdit, 0, 1+emptyDirCount+fileCount)
	edits = append(edits, &corev1.FileEdit{Op: "mkdir", Path: root})
	for i := range emptyDirCount {
		edits = append(edits, &corev1.FileEdit{Op: "mkdir", Path: fmt.Sprintf("%s/empty_%05d", root, i)})
	}
	for i := range fileCount {
		edit := *sharedFile
		edit.Path = fmt.Sprintf("%s/file_%05d.txt", root, i)
		edits = append(edits, &edit)
	}
	commitID, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "large directory seed", edits)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("large_directory_submit entries=%d files=%d empty_dirs=%d duration=%s", len(edits), fileCount, emptyDirCount, duration)

	expected := fileCount + emptyDirCount
	globalEntries, globalPages, err := collectLoadDirectoryPages(clients.ctx, clients.repo, &corev1.ListDirectoryRequest{
		CommitId: commitID,
		Path:     root,
		PageSize: int32(pageSize),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(globalEntries) != expected {
		t.Fatalf("global directory entries = %d, want %d", len(globalEntries), expected)
	}
	assertLoadDirectoryPages(t, "global_large_directory", globalEntries, globalPages, pageSize)

	sliceEntries, slicePages, err := collectLoadDirectoryPages(clients.ctx, clients.repo, &corev1.ListDirectoryRequest{
		CommitId: commitID,
		Path:     root,
		PageSize: int32(pageSize),
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sliceEntries) != expected {
		t.Fatalf("slice directory entries = %d, want %d", len(sliceEntries), expected)
	}
	assertLoadDirectoryPages(t, "slice_large_directory", sliceEntries, slicePages, pageSize)
	if err := requireEntry(sliceEntries, fmt.Sprintf("%s/empty_%05d", root, emptyDirCount-1), corev1.EntryKind_ENTRY_KIND_DIRECTORY); err != nil {
		t.Fatal(err)
	}
	if err := requireEntry(sliceEntries, fmt.Sprintf("%s/file_%05d.txt", root, fileCount-1), corev1.EntryKind_ENTRY_KIND_FILE); err != nil {
		t.Fatal(err)
	}

	db, err := postgres.Open(ctx, databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objectStore, err := filesystem.New(ts.objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.SetTreeStore(treestore.New(objectStore))
	assertLoadIntegrity(t, ctx, db, objectStore)
}

func TestLoadLargeDirectoryRenameIntegrity(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, ts, conn)

	fileCount := envInt("GITSLICE_LOAD_RENAME_DIR_FILES", 500)
	emptyDirCount := envInt("GITSLICE_LOAD_RENAME_DIR_EMPTY_DIRS", 50)
	sourceRoot := "/acme/payment/rename-src"
	movedRoot := "/acme/payment/rename-moved"
	sharedFile, err := loadUploadFileEdit(clients, sourceRoot+"/seed.txt", []byte("large rename shared content\n"))
	if err != nil {
		t.Fatal(err)
	}
	edits := make([]*corev1.FileEdit, 0, 2+emptyDirCount+fileCount)
	edits = append(edits,
		&corev1.FileEdit{Op: "mkdir", Path: sourceRoot},
		&corev1.FileEdit{Op: "mkdir", Path: sourceRoot + "/nested"},
	)
	for i := range emptyDirCount {
		edits = append(edits, &corev1.FileEdit{Op: "mkdir", Path: fmt.Sprintf("%s/nested/empty_%05d", sourceRoot, i)})
	}
	for i := range fileCount {
		edit := *sharedFile
		edit.Path = fmt.Sprintf("%s/nested/file_%05d.txt", sourceRoot, i)
		edits = append(edits, &edit)
	}
	if _, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "large rename seed", edits); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("large_directory_rename_seed entries=%d files=%d empty_dirs=%d duration=%s", len(edits), fileCount, emptyDirCount, duration)
	}

	commitID, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "large directory rename", []*corev1.FileEdit{{
		Op:      "rename",
		OldPath: sourceRoot,
		Path:    movedRoot,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("large_directory_rename_publish files=%d empty_dirs=%d duration=%s", fileCount, emptyDirCount, duration)

	movedEntries, movedPages, err := collectLoadDirectoryPages(clients.ctx, clients.repo, &corev1.ListDirectoryRequest{
		CommitId: commitID,
		Path:     movedRoot + "/nested",
		PageSize: 101,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := fileCount + emptyDirCount
	if len(movedEntries) != expected {
		t.Fatalf("moved directory entries = %d, want %d", len(movedEntries), expected)
	}
	assertLoadDirectoryPages(t, "renamed_large_directory", movedEntries, movedPages, 101)
	if err := requireEntry(movedEntries, fmt.Sprintf("%s/nested/file_%05d.txt", movedRoot, fileCount-1), corev1.EntryKind_ENTRY_KIND_FILE); err != nil {
		t.Fatal(err)
	}

	db, err := postgres.Open(ctx, databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objectStore, err := filesystem.New(ts.objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.SetTreeStore(treestore.New(objectStore))
	assertLoadIntegrity(t, ctx, db, objectStore)
}

func TestLoadLargeFileUploadCommitAndRead(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, ts, conn)

	size := envInt("GITSLICE_LOAD_LARGE_FILE_BYTES", 8*1024*1024)
	data := deterministicLoadBytes(size)
	path := "/acme/payment/large-file/blob.bin"
	edit, err := loadUploadFileEdit(clients, path, data)
	if err != nil {
		t.Fatal(err)
	}
	commitID, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "large file upload", []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/large-file"},
		edit,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("large_file_submit bytes=%d duration=%s", size, duration)

	fullBegin := time.Now()
	full, err := clients.repo.ReadFile(clients.ctx, &corev1.ReadFileRequest{CommitId: commitID, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(full.Data, data) {
		t.Fatalf("full read bytes mismatch: got %d bytes want %d", len(full.Data), len(data))
	}
	t.Logf("large_file_full_read bytes=%d duration=%s", len(full.Data), time.Since(fullBegin))

	offset := int64(size / 2)
	length := int64(minInt(64*1024, size/2))
	partialBegin := time.Now()
	partial, err := clients.repo.ReadFile(clients.ctx, &corev1.ReadFileRequest{CommitId: commitID, Path: path, Offset: offset, Length: length})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(partial.Data, data[int(offset):int(offset+length)]) {
		t.Fatalf("partial read bytes mismatch: got %d bytes want %d", len(partial.Data), length)
	}
	t.Logf("large_file_partial_read bytes=%d offset=%d duration=%s", len(partial.Data), offset, time.Since(partialBegin))
}

func TestLoadManySequentialCommitsAndHistoryPagination(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, ts, conn)

	commitCount := envInt("GITSLICE_LOAD_MANY_COMMITS", 180)
	pageSize := envInt("GITSLICE_LOAD_MANY_COMMITS_PAGE_SIZE", 37)
	path := "/acme/payment/history/many.txt"
	var commitIDs []string
	durations := make(chan time.Duration, commitCount)
	start := time.Now()
	for i := range commitCount {
		edit, err := loadUploadFileEdit(clients, path, []byte(fmt.Sprintf("version=%05d\n", i)))
		if err != nil {
			t.Fatal(err)
		}
		edits := []*corev1.FileEdit{edit}
		if i == 0 {
			edits = []*corev1.FileEdit{{Op: "mkdir", Path: "/acme/payment/history"}, edit}
		}
		commitID, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, fmt.Sprintf("history commit %05d", i), edits)
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		commitIDs = append(commitIDs, commitID)
		durations <- duration
	}
	close(durations)
	reportDurations(t, "many_sequential_commits_submit", commitCount, time.Since(start), drainDurations(durations))

	historyStart := time.Now()
	commits, pages, err := collectLoadCommitPages(clients.ctx, clients.repo, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Limit:   int32(pageSize),
		Path:    path,
		Slice:   &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("many_commits_history pages=%d commits=%d duration=%s", pages, len(commits), time.Since(historyStart))
	if len(commits) != commitCount {
		t.Fatalf("history commits = %d, want %d", len(commits), commitCount)
	}
	seen := map[string]struct{}{}
	for i, commit := range commits {
		want := commitIDs[len(commitIDs)-1-i]
		if commit.Id != want {
			t.Fatalf("history commit %d = %s, want %s", i, commit.Id, want)
		}
		if _, ok := seen[commit.Id]; ok {
			t.Fatalf("duplicate commit in history: %s", commit.Id)
		}
		seen[commit.Id] = struct{}{}
	}
	if pages <= 1 {
		t.Fatalf("history pagination returned %d pages, want multiple", pages)
	}
}

type loadDirectoryPage struct {
	count      int
	cursor     string
	nextCursor string
}

func collectLoadDirectoryPages(ctx context.Context, repo corev1.RepositoryServiceClient, req *corev1.ListDirectoryRequest) ([]*corev1.TreeEntry, []loadDirectoryPage, error) {
	pageReq := *req
	var entries []*corev1.TreeEntry
	var pages []loadDirectoryPage
	seen := map[string]struct{}{}
	for {
		page, err := repo.ListDirectory(ctx, &pageReq)
		if err != nil {
			return nil, nil, err
		}
		pages = append(pages, loadDirectoryPage{count: len(page.Entries), cursor: pageReq.Cursor, nextCursor: page.NextCursor})
		for _, entry := range page.Entries {
			if _, ok := seen[entry.Path]; ok {
				return nil, nil, fmt.Errorf("duplicate directory entry across pages: %s", entry.Path)
			}
			seen[entry.Path] = struct{}{}
			entries = append(entries, entry)
		}
		if page.NextCursor == "" {
			return entries, pages, nil
		}
		if page.NextCursor == pageReq.Cursor {
			return nil, nil, fmt.Errorf("directory cursor did not advance for %s", req.Path)
		}
		pageReq.Cursor = page.NextCursor
	}
}

func assertLoadDirectoryPages(t *testing.T, label string, entries []*corev1.TreeEntry, pages []loadDirectoryPage, pageSize int) {
	t.Helper()
	if len(pages) == 0 {
		t.Fatalf("%s returned no pages", label)
	}
	for i, page := range pages {
		if page.count > pageSize {
			t.Fatalf("%s page %d count = %d, want <= %d", label, i, page.count, pageSize)
		}
		if i < len(pages)-1 && page.nextCursor == "" {
			t.Fatalf("%s page %d had empty next cursor before final page", label, i)
		}
	}
	t.Logf("%s entries=%d pages=%d page_size=%d", label, len(entries), len(pages), pageSize)
}

func deterministicLoadBytes(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*31 + 17) % 251)
	}
	return data
}

func collectLoadCommitPages(ctx context.Context, repo corev1.RepositoryServiceClient, req *corev1.ListCommitsRequest) ([]*corev1.Commit, int, error) {
	pageReq := *req
	var commits []*corev1.Commit
	seen := map[string]struct{}{}
	pages := 0
	for {
		page, err := repo.ListCommits(ctx, &pageReq)
		if err != nil {
			return nil, 0, err
		}
		pages++
		for _, commit := range page.Commits {
			if _, ok := seen[commit.Id]; ok {
				return nil, 0, fmt.Errorf("duplicate commit across pages: %s", commit.Id)
			}
			seen[commit.Id] = struct{}{}
			commits = append(commits, commit)
		}
		if page.NextPageToken == "" {
			return commits, pages, nil
		}
		if page.NextPageToken == pageReq.PageToken {
			return nil, 0, fmt.Errorf("commit page token did not advance")
		}
		pageReq.PageToken = page.NextPageToken
	}
}
