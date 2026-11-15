package cli

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestWorkspaceConfinementRejectsReservedGsPaths(t *testing.T) {
	root := t.TempDir()
	ws := WorkspaceConfig{
		Account:       "acme",
		Slice:         "app",
		IncludedPaths: []string{"/"},
	}
	for _, badGlobal := range []string{
		"/.gs",
		"/.gs/slice.json",
		"/.gs/config",
		"/acme/app/.gs",
		"/acme/app/.gs/slice.json",
	} {
		if _, err := workspaceRelPath(ws, badGlobal); err == nil {
			t.Fatalf("workspaceRelPath(%q) unexpectedly succeeded", badGlobal)
		}
		h := &workspaceHydrator{root: root, ws: ws}
		if _, _, _, err := h.fileTarget(&corev1.TreeEntry{Path: badGlobal, Mode: 0o100644}); err == nil {
			t.Fatalf("fileTarget(%q) unexpectedly succeeded", badGlobal)
		}
	}

	subWS := WorkspaceConfig{
		Account:       "acme",
		Slice:         "app",
		IncludedPaths: []string{"/acme/app"},
	}
	for _, badInput := range []string{".gs", ".gs/slice.json", "nested/.gs/hooks"} {
		if _, err := workspaceRelativePathToGlobalPath(subWS, badInput); err == nil {
			t.Fatalf("workspaceRelativePathToGlobalPath(%q) unexpectedly succeeded", badInput)
		}
	}

	for _, badCheckRel := range []string{".gs", ".gs/slice.json", "sub/.gs/state.json"} {
		if _, err := checkTreeTarget(root, badCheckRel); err == nil {
			t.Fatalf("checkTreeTarget(%q) unexpectedly succeeded", badCheckRel)
		}
	}
}

func TestWorkspaceConfinementValidatesRelativeSymlinks(t *testing.T) {
	parent := t.TempDir()
	outsideFile := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "ws")
	if err := os.MkdirAll(filepath.Join(root, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gs", "slice.json"), []byte(`{"account":"acme"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Safe relative symlink inside workspace stays a symlink.
	safePath, _, err := writeConfinedWorkspaceFile(root, "sub/readme-link", 0o120000, strings.NewReader("../README.md"))
	if err != nil {
		t.Fatalf("safe symlink write failed: %v", err)
	}
	info, err := os.Lstat(safePath)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("expected real symlink for safe relative target, got info=%v err=%v", info, err)
	}

	// Escaping relative symlink falls back to a plain regular file.
	escapePath, _, err := writeConfinedWorkspaceFile(root, "sub/escape-link", 0o120000, strings.NewReader("../../outside.txt"))
	if err != nil {
		t.Fatalf("escaping symlink fallback write failed: %v", err)
	}
	escapeInfo, err := os.Lstat(escapePath)
	if err != nil {
		t.Fatal(err)
	}
	if escapeInfo.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("escaping relative symlink was created as an OS symlink")
	}
	gotData, err := os.ReadFile(escapePath)
	if err != nil || string(gotData) != "../../outside.txt" {
		t.Fatalf("fallback file content = %q, err=%v", string(gotData), err)
	}

	// Relative symlink into .gs/ also falls back to a plain regular file.
	gsLinkPath, _, err := writeConfinedWorkspaceFile(root, "sub/gs-link", 0o120000, strings.NewReader("../.gs/slice.json"))
	if err != nil {
		t.Fatalf(".gs symlink fallback write failed: %v", err)
	}
	gsInfo, err := os.Lstat(gsLinkPath)
	if err != nil {
		t.Fatal(err)
	}
	if gsInfo.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("symlink targeting .gs/ was created as an OS symlink")
	}
}

func TestWorkspaceConfinementPreventsIntermediateAndLeafSymlinkOverwrite(t *testing.T) {
	parent := t.TempDir()
	outsideDir := filepath.Join(parent, "outside-dir")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victimFile := filepath.Join(outsideDir, "victim.txt")
	if err := os.WriteFile(victimFile, []byte("DO NOT OVERWRITE"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(parent, "ws")
	if err := os.MkdirAll(filepath.Join(root, "acme", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := WorkspaceConfig{
		Account:       "acme",
		Slice:         "app",
		IncludedPaths: []string{"/acme/app"},
	}
	cache, err := clientcache.New(filepath.Join(parent, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	r := Runner{Home: parent, Dir: root}

	// 1. Intermediate symlink directory must be rejected across remote write, merge write, and conflict write.
	symlinkDir := filepath.Join(root, "acme", "app", "linked-dir")
	if err := os.Symlink(outsideDir, symlinkDir); err != nil {
		t.Fatal(err)
	}
	payload := []byte("attacker remote content\n")
	cachedObj, err := cache.PutBytes(payload)
	if err != nil {
		t.Fatal(err)
	}

	err = r.writeWorkspaceRemoteFile(context.Background(), nil, cache, root, ws, nil, "c2", remoteWorkspaceFile{
		BaseSnapshotFile: BaseSnapshotFile{
			Path:        "/acme/app/linked-dir/victim.txt",
			RelPath:     "acme/app/linked-dir/victim.txt",
			ContentHash: cachedObj.ContentHash,
			Mode:        0o100644,
		},
	})
	if err == nil {
		t.Fatal("writeWorkspaceRemoteFile traversed intermediate symlink directory")
	}

	err = r.writeWorkspaceMergedFile(root, ws, nil, mergedWorkspaceFile{
		Path: "/acme/app/linked-dir/victim.txt",
		Data: payload,
		Mode: 0o100644,
	})
	if err == nil {
		t.Fatal("writeWorkspaceMergedFile traversed intermediate symlink directory")
	}

	baseBytes := []byte("base\n")
	localBytes := []byte("local\n")
	remoteBytes := []byte("remote\n")
	baseObj, _ := cache.PutBytes(baseBytes)
	localObj, _ := cache.PutBytes(localBytes)
	remoteObj, _ := cache.PutBytes(remoteBytes)

	conflictPath := "/acme/app/linked-dir/victim.txt"
	err = r.materializeWorkspaceConflict(
		context.Background(),
		nil,
		cache,
		root,
		ws,
		"c1",
		"c2",
		BaseSnapshot{Files: map[string]BaseSnapshotFile{conflictPath: {Path: conflictPath, ContentHash: baseObj.ContentHash, Mode: 0o100644}}},
		map[string]workingFile{conflictPath: {BaseSnapshotFile: BaseSnapshotFile{Path: conflictPath, ContentHash: localObj.ContentHash, Mode: 0o100644}, AbsPath: filepath.Join(symlinkDir, "victim.txt")}},
		map[string]remoteWorkspaceFile{conflictPath: {BaseSnapshotFile: BaseSnapshotFile{Path: conflictPath, ContentHash: remoteObj.ContentHash, Mode: 0o100644}}},
		WorkspaceConflict{Path: conflictPath},
	)
	if err == nil {
		t.Fatal("materializeWorkspaceConflict traversed intermediate symlink directory")
	}

	// 2. Leaf symlink pointing outside workspace is unlinked and replaced with a regular file without modifying victimFile.
	leafLink := filepath.Join(root, "acme", "app", "leaf.txt")
	if err := os.Symlink(victimFile, leafLink); err != nil {
		t.Fatal(err)
	}
	if err := r.writeWorkspaceRemoteFile(context.Background(), nil, cache, root, ws, nil, "c2", remoteWorkspaceFile{
		BaseSnapshotFile: BaseSnapshotFile{
			Path:        "/acme/app/leaf.txt",
			RelPath:     "acme/app/leaf.txt",
			ContentHash: cachedObj.ContentHash,
			Mode:        0o100644,
		},
	}); err != nil {
		t.Fatalf("writeWorkspaceRemoteFile on leaf symlink failed: %v", err)
	}
	leafInfo, err := os.Lstat(leafLink)
	if err != nil || leafInfo.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("expected leaf symlink to be replaced with regular file, got info=%v err=%v", leafInfo, err)
	}
	victimAfter, err := os.ReadFile(victimFile)
	if err != nil || string(victimAfter) != "DO NOT OVERWRITE" {
		t.Fatalf("external victim file was modified: %q (err=%v)", string(victimAfter), err)
	}

	// Re-link leaf and verify materializeWorkspaceConflict also unlinks instead of overwriting or reading through symlink.
	_ = os.Remove(leafLink)
	if err := os.Symlink(victimFile, leafLink); err != nil {
		t.Fatal(err)
	}
	leafGlobal := "/acme/app/leaf.txt"
	if err := r.materializeWorkspaceConflict(
		context.Background(),
		nil,
		cache,
		root,
		ws,
		"c1",
		"c2",
		BaseSnapshot{Files: map[string]BaseSnapshotFile{leafGlobal: {Path: leafGlobal, ContentHash: baseObj.ContentHash, Mode: 0o100644}}},
		map[string]workingFile{leafGlobal: {BaseSnapshotFile: BaseSnapshotFile{Path: leafGlobal, ContentHash: objectid.RawContentHash([]byte(victimFile)), Mode: 0o120000}, AbsPath: leafLink}},
		map[string]remoteWorkspaceFile{leafGlobal: {BaseSnapshotFile: BaseSnapshotFile{Path: leafGlobal, ContentHash: remoteObj.ContentHash, Mode: 0o100644}}},
		WorkspaceConflict{Path: leafGlobal},
	); err != nil {
		t.Fatalf("materializeWorkspaceConflict on leaf symlink failed: %v", err)
	}
	leafInfo, err = os.Lstat(leafLink)
	if err != nil || leafInfo.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("expected conflict file to replace leaf symlink with regular file, got %v", leafInfo)
	}
	conflictWritten, err := os.ReadFile(leafLink)
	if err != nil || strings.Contains(string(conflictWritten), "DO NOT OVERWRITE") {
		t.Fatalf("conflict file leaked external symlink target contents: %q (err=%v)", string(conflictWritten), err)
	}
	victimAfter, err = os.ReadFile(victimFile)
	if err != nil || string(victimAfter) != "DO NOT OVERWRITE" {
		t.Fatalf("external victim file modified during conflict write: %q", string(victimAfter))
	}
}
