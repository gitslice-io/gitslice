package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/checkexec"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestMaterializeCheckTreeFetchesNestedPrefix(t *testing.T) {
	repo := checkTreeRepoClient{
		account: "acme",
		inner: newFakeCheckRepo(map[string][]*corev1.TreeEntry{
			"/acme": {
				checkDirEntry("/acme/backend"),
			},
			"/acme/backend": {
				checkDirEntry("/acme/backend/shared"),
			},
			"/acme/backend/shared": {
				checkFileEntry("/acme/backend/shared/config.sh", 0o100755),
			},
		}, map[string][]byte{
			"/acme/backend/shared/config.sh": []byte("echo from tree\n"),
		}),
	}

	dest := t.TempDir()
	d := newTestCheckDaemon(t)
	if err := d.materializeCheckTree(context.Background(), repo, "tree-1", []string{"backend", "backend/shared"}, dest); err != nil {
		t.Fatalf("materializeCheckTree() error = %v", err)
	}

	target := filepath.Join(dest, "backend", "shared", "config.sh")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "echo from tree\n" {
		t.Fatalf("materialized content = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("materialized mode = %#o, want 0755", got)
	}
}

func TestMaterializeCheckTreeFetchesNestedFilesConcurrently(t *testing.T) {
	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{
		"/": {
			checkDirEntry("/app"),
			checkFileEntry("/README.md", 0o100644),
		},
		"/app": {
			checkDirEntry("/app/lib"),
			checkFileEntry("/app/main.sh", 0o100755),
		},
		"/app/lib": {
			checkFileEntry("/app/lib/a.txt", 0o100644),
			checkFileEntry("/app/lib/b.txt", 0o100644),
		},
	}, map[string][]byte{
		"/README.md":     []byte("readme\n"),
		"/app/main.sh":   []byte("#!/bin/sh\n"),
		"/app/lib/a.txt": []byte("alpha\n"),
		"/app/lib/b.txt": []byte("bravo\n"),
	})
	repo.readStarted = make(chan struct{}, 4)
	repo.readRelease = make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(repo.readRelease)
		}
	}()

	dest := t.TempDir()
	d := newTestCheckDaemon(t)
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.materializeCheckTree(context.Background(), repo, "tree-1", []string{"app", "README.md"}, dest)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-repo.readStarted:
		case err := <-errCh:
			t.Fatalf("materializeCheckTree() returned before concurrent reads: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent ReadFile calls")
		}
	}
	close(repo.readRelease)
	released = true
	if err := <-errCh; err != nil {
		t.Fatalf("materializeCheckTree() error = %v", err)
	}
	if got := repo.maxConcurrentReads(); got < 2 {
		t.Fatalf("max concurrent ReadFile calls = %d, want at least 2", got)
	}

	assertFileContent(t, filepath.Join(dest, "README.md"), "readme\n")
	assertFileContent(t, filepath.Join(dest, "app", "main.sh"), "#!/bin/sh\n")
	assertFileContent(t, filepath.Join(dest, "app", "lib", "a.txt"), "alpha\n")
	assertFileContent(t, filepath.Join(dest, "app", "lib", "b.txt"), "bravo\n")
	assertFileMode(t, filepath.Join(dest, "app", "main.sh"), 0o755)
	assertFileMode(t, filepath.Join(dest, "app", "lib", "a.txt"), 0o644)
}

func TestMaterializeCheckTreeWalksDirectoriesConcurrently(t *testing.T) {
	const branchCount = 18

	dirs := map[string][]*corev1.TreeEntry{
		"/": {},
	}
	files := map[string][]byte{}
	blockedListPaths := map[string]struct{}{}
	expectedDirs := map[string]struct{}{
		"/": {},
	}
	expectedFiles := map[string]string{}

	addFile := func(p, content string) {
		files[p] = []byte(content)
		expectedFiles[p] = content
	}
	for i := 0; i < branchCount; i++ {
		module := fmt.Sprintf("/module-%02d", i)
		src := module + "/src"
		pkg := src + "/pkg"
		dirs["/"] = append(dirs["/"], checkDirEntry(module))
		dirs[module] = []*corev1.TreeEntry{
			checkDirEntry(src),
			checkFileEntry(module+"/README.md", 0o100644),
		}
		dirs[src] = []*corev1.TreeEntry{
			checkDirEntry(pkg),
			checkFileEntry(src+"/main.go", 0o100644),
		}
		dirs[pkg] = []*corev1.TreeEntry{
			checkFileEntry(pkg+"/leaf.txt", 0o100644),
		}
		blockedListPaths[module] = struct{}{}
		expectedDirs[module] = struct{}{}
		expectedDirs[src] = struct{}{}
		expectedDirs[pkg] = struct{}{}
		addFile(module+"/README.md", fmt.Sprintf("module %02d\n", i))
		addFile(src+"/main.go", fmt.Sprintf("package module%02d\n", i))
		addFile(pkg+"/leaf.txt", fmt.Sprintf("leaf %02d\n", i))
	}

	repo := newFakeCheckRepo(dirs, files)
	repo.listStarted = make(chan string, branchCount+1)
	repo.listRelease = make(chan struct{})
	repo.listBlockedPaths = blockedListPaths
	released := false
	defer func() {
		if !released {
			close(repo.listRelease)
		}
	}()

	dest := t.TempDir()
	d := newTestCheckDaemon(t)
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.materializeCheckTree(context.Background(), repo, "tree-1", []string{"/"}, dest)
	}()

	startedBlockedPaths := map[string]struct{}{}
	for len(startedBlockedPaths) < 2 {
		select {
		case p := <-repo.listStarted:
			if _, ok := blockedListPaths[p]; ok {
				startedBlockedPaths[p] = struct{}{}
			}
		case err := <-errCh:
			t.Fatalf("materializeCheckTree() returned before concurrent directory listings: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent ListDirectory calls")
		}
	}

	close(repo.listRelease)
	released = true
	if err := <-errCh; err != nil {
		t.Fatalf("materializeCheckTree() error = %v", err)
	}
	if got := repo.maxConcurrentLists(); got < 2 {
		t.Fatalf("max concurrent ListDirectory calls = %d, want at least 2", got)
	}

	for dir := range expectedDirs {
		target, err := checkTreeLocalPath(dest, dir)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", target)
		}
	}
	for filePath, want := range expectedFiles {
		target, err := checkTreeLocalPath(dest, filePath)
		if err != nil {
			t.Fatal(err)
		}
		assertFileContent(t, target, want)
	}
	if got := repo.readCallCount(); got != len(expectedFiles) {
		t.Fatalf("ReadFile calls = %d, want %d", got, len(expectedFiles))
	}
	listCalls := repo.listCallCounts()
	if got := len(listCalls); got != len(dirs) {
		t.Fatalf("listed directories = %d, want %d: %#v", got, len(dirs), listCalls)
	}
	for dir := range dirs {
		if got := listCalls[dir]; got != 1 {
			t.Fatalf("ListDirectory calls for %s = %d, want 1", dir, got)
		}
	}
}

func TestMaterializeCheckTreeReusesObjectCacheAcrossRuns(t *testing.T) {
	readme := []byte("readme\n")
	main := []byte("#!/bin/sh\necho app\n")
	readmeEntry := checkFileEntry("/README.md", 0o100644)
	readmeEntry.ContentHash = objectid.RawContentHash(readme)
	mainEntry := checkFileEntry("/app/main.sh", 0o100755)
	mainEntry.ContentHash = objectid.RawContentHash(main)
	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{
		"/": {
			checkDirEntry("/app"),
			readmeEntry,
		},
		"/app": {
			mainEntry,
		},
	}, map[string][]byte{
		"/README.md":   readme,
		"/app/main.sh": main,
	})
	d := newTestCheckDaemon(t)

	firstDest := t.TempDir()
	if err := d.materializeCheckTree(context.Background(), repo, "tree-1", []string{"/"}, firstDest); err != nil {
		t.Fatalf("first materializeCheckTree() error = %v", err)
	}
	firstReads := repo.readCallCount()
	if firstReads != 2 {
		t.Fatalf("first ReadFile calls = %d, want 2", firstReads)
	}

	secondDest := t.TempDir()
	if err := d.materializeCheckTree(context.Background(), repo, "tree-1", []string{"/"}, secondDest); err != nil {
		t.Fatalf("second materializeCheckTree() error = %v", err)
	}
	secondReads := repo.readCallCount() - firstReads
	if secondReads != 0 {
		t.Fatalf("second ReadFile calls = %d, want 0 cache hits", secondReads)
	}
	assertFileContent(t, filepath.Join(secondDest, "README.md"), string(readme))
	assertFileContent(t, filepath.Join(secondDest, "app", "main.sh"), string(main))
	assertFileMode(t, filepath.Join(secondDest, "app", "main.sh"), 0o755)
}

func TestCheckRunSpecMaterializedHostExecution(t *testing.T) {
	repo := checkTreeRepoClient{
		account: "acme",
		inner: newFakeCheckRepo(map[string][]*corev1.TreeEntry{
			"/acme": {
				checkDirEntry("/acme/backend"),
			},
			"/acme/backend": {
				checkFileEntry("/acme/backend/message.txt", 0o100644),
			},
		}, map[string][]byte{
			"/acme/backend/message.txt": []byte("hello from result tree\n"),
		}),
	}
	spec := &corev1.CheckRunSpec{
		RunId:            "run-1",
		Name:             "backend/cat",
		Command:          "cat backend/message.txt",
		MaterializePaths: []string{"backend/message.txt"},
	}

	dest := t.TempDir()
	d := newTestCheckDaemon(t)
	if err := d.materializeCheckTree(context.Background(), repo, "tree-1", spec.GetMaterializePaths(), dest); err != nil {
		t.Fatalf("materializeCheckTree() error = %v", err)
	}
	result, err := checkexec.Run(context.Background(), dest, checkSpecFromProto(spec))
	if err != nil {
		t.Fatalf("checkexec.Run() error = %v", err)
	}
	if result.Status != "passed" || result.ExitCode != 0 {
		t.Fatalf("result = %#v, want passed exit 0", result)
	}
	if !strings.Contains(result.Log, "hello from result tree") {
		t.Fatalf("result log = %q, want materialized file content", result.Log)
	}
}

func TestCheckRunSpecMapsSetup(t *testing.T) {
	spec := checkSpecFromProto(&corev1.CheckRunSpec{
		Setup:  []string{"true"},
		Cache:  []string{"/go/pkg/mod"},
		Memory: "8g",
		Cpus:   "4",
	})
	if got := spec.Setup; len(got) != 1 || got[0] != "true" {
		t.Fatalf("Setup = %#v, want [true]", got)
	}
	if got := spec.Cache; len(got) != 1 || got[0] != "/go/pkg/mod" {
		t.Fatalf("Cache = %#v, want [/go/pkg/mod]", got)
	}
	if spec.Memory != "8g" {
		t.Fatalf("Memory = %q, want 8g", spec.Memory)
	}
	if spec.CPUs != "4" {
		t.Fatalf("CPUs = %q, want 4", spec.CPUs)
	}
}
