package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestCLISignupShellDefaultsToPersonalHome(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	outsideWorkspace := t.TempDir()
	homeWorkspace := t.TempDir()

	runCLISignupThroughWeb(t, home, outsideWorkspace, ts, "other_user")
	runCLI(t, home, outsideWorkspace, "fs", "mkdir", "/other-user/docs")
	runCLISignupThroughWeb(t, home, outsideWorkspace, ts, "shell_user")
	initOutput := runCLI(t, home, homeWorkspace, "init", "home")
	if !strings.Contains(initOutput, "initialized workspace for shell-user:home") {
		t.Fatalf("unexpected home workspace init output:\n%s", initOutput)
	}
	paths := strings.TrimSpace(runCLI(t, home, outsideWorkspace, "slice", "paths", "home"))
	if paths != "/shell-user" {
		t.Fatalf("bare slice paths = %q, want /shell-user", paths)
	}
	stdout, stderr := runCLIStreamsWithInput(t, home, outsideWorkspace, strings.Join([]string{
		"pwd",
		"ls",
		"cd other-user",
		"cd shell-user",
		"pwd",
		"quit",
	}, "\n")+"\n", "shell")
	if !strings.Contains(stderr, "path is outside the attached slice: /other-user") {
		t.Fatalf("expected other-user to be hidden from shell-user home shell, got stderr:\n%s", stderr)
	}
	for _, want := range []string{
		"server shell: shell-user:home @",
		"shell-user/",
		"/shell-user",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("signup shell output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "other-user/") {
		t.Fatalf("shell-user home shell leaked other-user folder:\n%s", stdout)
	}
	explicit, explicitStderr := runCLIStreamsWithInput(t, home, outsideWorkspace, "pwd\nquit\n", "shell", "--slice", "home")
	if explicitStderr != "" {
		t.Fatalf("expected empty explicit bare-slice shell stderr, got:\n%s", explicitStderr)
	}
	if !strings.Contains(explicit, "server shell: shell-user:home @") || !strings.Contains(explicit, "/") {
		t.Fatalf("explicit bare-slice shell output missing home scope:\n%s", explicit)
	}
}

func TestCLIFileAndShellMutationsStayInHome(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	dir := t.TempDir()

	runCLISignupThroughWeb(t, home, dir, ts, "file_user")
	runCLI(t, home, dir, "fs", "mkdir", "/file-user/docs")
	runCLI(t, home, dir, "fs", "write", "/file-user/docs/readme.md", "--text", "hello from file command\n")
	runCLI(t, home, dir, "fs", "mv", "/file-user/docs/readme.md", "/file-user/docs/today.md")
	runCLI(t, home, dir, "fs", "mkdir", "/file-user/empty")
	runCLI(t, home, dir, "fs", "mv", "/file-user/empty", "/file-user/archive")

	fsList, fsListStderr := runCLIStreams(t, home, dir, "fs", "ls", "/file-user")
	if fsListStderr != "" {
		t.Fatalf("explicit fs ls should not print a default-path diagnostic, got:\n%s", fsListStderr)
	}
	for _, want := range []string{"archive/", "docs/"} {
		if !strings.Contains(fsList, want) {
			t.Fatalf("fs ls output missing %q:\n%s", want, fsList)
		}
	}
	defaultFSList, defaultFSListStderr := runCLIStreams(t, home, dir, "fs", "ls")
	if !strings.Contains(defaultFSListStderr, "remote: listing file-user:home at /file-user") {
		t.Fatalf("default fs ls stderr should name the remote home root, got:\n%s", defaultFSListStderr)
	}
	for _, want := range []string{"archive/", "docs/"} {
		if !strings.Contains(defaultFSList, want) {
			t.Fatalf("default fs ls output missing %q:\n%s", want, defaultFSList)
		}
	}
	defaultFSListJSON, defaultFSListJSONStderr := runCLIStreams(t, home, dir, "--json", "fs", "ls")
	if defaultFSListJSONStderr != "" {
		t.Fatalf("json fs ls should keep stderr empty, got:\n%s", defaultFSListJSONStderr)
	}
	if !strings.Contains(defaultFSListJSON, `"path": "/file-user"`) {
		t.Fatalf("json fs ls should report the resolved remote path, got:\n%s", defaultFSListJSON)
	}
	fsCat := runCLI(t, home, dir, "fs", "cat", "/file-user/docs/today.md")
	if fsCat != "hello from file command\n" {
		t.Fatalf("unexpected fs cat output:\n%s", fsCat)
	}
	followHistory := runCLI(t, home, dir, "log", "--", "/file-user/docs/today.md")
	if !strings.Contains(followHistory, "file write /file-user/docs/readme.md") || !strings.Contains(followHistory, "file mv") {
		t.Fatalf("follow-moves commit history missing write or move:\n%s", followHistory)
	}
	literalHistory := runCLI(t, home, dir, "log", "--no-follow-moves", "--", "/file-user/docs/today.md")
	if strings.Contains(literalHistory, "file write /file-user/docs/readme.md") || !strings.Contains(literalHistory, "file mv") {
		t.Fatalf("literal commit history should include move but not pre-move write:\n%s", literalHistory)
	}

	stdout, stderr := runCLIStreamsWithInput(t, home, dir, strings.Join([]string{
		"ls /file-user",
		"ls /file-user/docs",
		"cat /file-user/docs/today.md",
		"cd /file-user/docs",
		"pwd",
		"cat today.md",
		"cd /file-user",
		"mkdir /file-user/shell",
		"cd shell",
		"write note.txt hello from shell",
		"mv note.txt final.txt",
		"cat final.txt",
		"rm final.txt",
		"ls /file-user/shell",
		"quit",
	}, "\n")+"\n", "shell")
	if stderr != "" {
		t.Fatalf("expected empty shell stderr, got:\n%s", stderr)
	}
	for _, want := range []string{
		"archive/",
		"docs/",
		"today.md",
		"hello from file command\n",
		"gs /file-user/docs> /file-user/docs",
		"ok created through changeset",
		"ok wrote through changeset",
		"ok moved through changeset",
		"ok removed through changeset",
		"hello from shell",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("mutation shell output missing %q:\n%s", want, stdout)
		}
	}

	_, stderr = runCLIFails(t, home, dir, "fs", "write", "relative.txt", "--text", "no")
	if !strings.Contains(stderr, "paths must be absolute") {
		t.Fatalf("expected absolute-path error, got:\n%s", stderr)
	}
	_, stderr = runCLIFails(t, home, dir, "fs", "write", "/alice/hack.txt", "--text", "no")
	if !strings.Contains(stderr, "outside the home slice") {
		t.Fatalf("expected outside-home error, got:\n%s", stderr)
	}
}

func TestCLIUploadLargeDirectory(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	dir := t.TempDir()

	runCLISignupThroughWeb(t, home, dir, ts, "upload_user")
	sourceRoot := filepath.Join(t.TempDir(), "source")
	targetFiles := uploadTestFileCount(t)
	filesPerDir := 8
	dirCount := (targetFiles + filesPerDir - 1) / filesPerDir
	totalFiles := 0
	for d := 0; d < dirCount; d++ {
		subdir := filepath.Join(sourceRoot, fmt.Sprintf("dir-%02d", d), fmt.Sprintf("sub-%02d", d%4))
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < filesPerDir && totalFiles < targetFiles; f++ {
			name := fmt.Sprintf("file-%02d.txt", f)
			content := fmt.Sprintf("dir=%02d sub=%02d file=%02d\n", d, d%4, f)
			if err := os.WriteFile(filepath.Join(subdir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			totalFiles++
		}
	}
	for i := 0; i < 10; i++ {
		if err := os.MkdirAll(filepath.Join(sourceRoot, "empty", fmt.Sprintf("leaf-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, stderr := runCLIFails(t, home, dir, "fs", "upload", sourceRoot, "/upload-user/not-recursive")
	if !strings.Contains(stderr, "Pass --recursive") {
		t.Fatalf("directory upload without --recursive stderr missing hint:\n%s", stderr)
	}

	output := runCLI(t, home, dir, "fs", "upload", sourceRoot, "/upload-user/bulk", "--recursive", "--concurrency", "8")
	wantChangedPaths := totalFiles + 10
	wantSummary := fmt.Sprintf("uploaded %d paths in upload-user:home", wantChangedPaths)
	if !strings.Contains(output, wantSummary) {
		t.Fatalf("unexpected upload output:\n%s", output)
	}
	lastDir := (targetFiles - 1) / filesPerDir
	lastFile := (targetFiles - 1) % filesPerDir
	lastSubdir := lastDir % 4
	lastRemotePath := fmt.Sprintf("/upload-user/bulk/dir-%02d/sub-%02d/file-%02d.txt", lastDir, lastSubdir, lastFile)
	wantContent := fmt.Sprintf("dir=%02d sub=%02d file=%02d\n", lastDir, lastSubdir, lastFile)
	if got := runCLI(t, home, dir, "fs", "cat", lastRemotePath); got != wantContent {
		t.Fatalf("uploaded file content = %q", got)
	}

	token := readToken(t, home)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	repo := corev1.NewRepositoryServiceClient(conn)
	ref, err := repo.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	files, dirs := countRemoteTree(t, ctx, repo, ref.CommitId, "/upload-user/bulk")
	if files != totalFiles {
		t.Fatalf("uploaded file count = %d, want %d", files, totalFiles)
	}
	wantMinDirs := dirCount*2 + 11
	if dirs < wantMinDirs {
		t.Fatalf("uploaded directory count = %d, want at least %d", dirs, wantMinDirs)
	}
	resolved, err := repo.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: ref.CommitId, Path: "/upload-user/bulk/empty/leaf-09"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		t.Fatalf("empty directory was not preserved: %#v", resolved.Entry)
	}

	singleFile := filepath.Join(t.TempDir(), "single.txt")
	if err := os.WriteFile(singleFile, []byte("single upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, dir, "fs", "upload", singleFile, "/upload-user/single.txt")
	if got := runCLI(t, home, dir, "fs", "cat", "/upload-user/single.txt"); got != "single upload\n" {
		t.Fatalf("single uploaded file content = %q", got)
	}

	_, stderr = runCLIFails(t, home, dir, "fs", "upload", singleFile, "/alice/hack.txt")
	if !strings.Contains(stderr, "outside the home slice") {
		t.Fatalf("expected outside-home upload error, got:\n%s", stderr)
	}
}
