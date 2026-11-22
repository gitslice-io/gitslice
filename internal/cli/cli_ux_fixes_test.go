package cli

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrintStackWithActivePrefersWorkspaceActive(t *testing.T) {
	stack := &corev1.ChangesetStack{
		Id:            "stk_123",
		Status:        "open",
		ActiveEntryId: "cs_bbbbbbbbbb",
		Entries: []*corev1.ChangesetStackEntry{
			{ChangesetId: "cs_aaaaaaaaaa", Depth: 0, Changeset: &corev1.Changeset{Id: "cs_aaaaaaaaaa", Status: "draft", CurrentPatchsetNumber: 2}},
			{ChangesetId: "cs_bbbbbbbbbb", Depth: 1, ParentChangesetId: "cs_aaaaaaaaaa", Changeset: &corev1.Changeset{Id: "cs_bbbbbbbbbb", Status: "draft", CurrentPatchsetNumber: 1}},
		},
	}
	var out bytes.Buffer
	printStackWithActive(&out, stack, "cs_aaaaaaaaaa")
	got := out.String()
	if !strings.Contains(got, "active: aaaaaaaaaa\n") {
		t.Fatalf("expected workspace active changeset aaaaaaaaaa, got:\n%s", got)
	}
	if strings.Contains(got, "active: bbbbbbbbbb\n") {
		t.Fatalf("unexpected server stack active changeset bbbbbbbbbb, got:\n%s", got)
	}
}

func TestCheckProgressWatchStreamsTransitionsAndFailsFastOnRequiredCheck(t *testing.T) {
	var stderr bytes.Buffer
	watch := newCheckProgressWatch(&stderr)
	required := map[string]bool{
		"gitslice/gofmt":     true,
		"gitslice/e2e":       true,
		"gitslice/web/build": true,
	}

	// Poll 1: queued stays quiet; running and passed print once.
	err := watch.observe("cs_1234567890", "gitslice/e2e", required, []*corev1.CheckRun{
		{Id: "run_1", CheckName: "gitslice/e2e", Status: "queued"},
		{Id: "run_2", CheckName: "gitslice/gofmt", Status: "passed"},
		{Id: "run_3", CheckName: "gitslice/web/build", Status: "running"},
		{Id: "run_opt", CheckName: "gitslice/optional", Status: "failed", Summary: "advisory only"},
	})
	if err != nil {
		t.Fatalf("non-required failure should not fail fast: %v", err)
	}
	got1 := stderr.String()
	if strings.Contains(got1, "gitslice/e2e: queued") {
		t.Fatalf("initial queued status should not spam stderr:\n%s", got1)
	}
	if !strings.Contains(got1, "check gitslice/gofmt: passed\n") || !strings.Contains(got1, "check gitslice/web/build: running\n") {
		t.Fatalf("expected transition lines in stderr:\n%s", got1)
	}

	// Poll 2: required check web/build fails while e2e is still running -> fail fast!
	stderr.Reset()
	err = watch.observe("cs_1234567890", "gitslice/e2e", required, []*corev1.CheckRun{
		{Id: "run_1", CheckName: "gitslice/e2e", Status: "running"},
		{Id: "run_2", CheckName: "gitslice/gofmt", Status: "passed"},
		{Id: "run_3", CheckName: "gitslice/web/build", Status: "failed", Summary: "exit 1"},
	})
	if err == nil {
		t.Fatal("expected fail-fast error when required check fails")
	}
	if !isUserErrorCode(err, "required_check_failed") {
		t.Fatalf("expected required_check_failed error, got: %v", err)
	}
	got2 := stderr.String()
	if strings.Contains(got2, "check gitslice/gofmt: passed") {
		t.Fatalf("unchanged passed check should not repeat:\n%s", got2)
	}
	if !strings.Contains(got2, "check gitslice/web/build: failed (run_3; exit 1)\n") {
		t.Fatalf("expected failure transition line:\n%s", got2)
	}
	if !strings.Contains(err.Error(), `required check "gitslice/web/build" failed`) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestParseAndPrintUnifiedDiffStatColor(t *testing.T) {
	diffText := strings.Join([]string{
		"diff --git a/acme/app/new.go b/acme/app/new.go",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/acme/app/new.go",
		"@@ -0,0 +1,3 @@",
		"+package app",
		"+",
		"+func Hi() {}",
		"diff --git a/acme/app/edit.go b/acme/app/edit.go",
		"--- a/acme/app/edit.go",
		"+++ b/acme/app/edit.go",
		"@@ -1,3 +1,3 @@",
		" package app",
		"-var x = 1",
		"+var x = 2",
		"+var y = 3",
		"diff --git a/acme/app/logo.png b/acme/app/logo.png",
		"Binary files a/acme/app/logo.png and b/acme/app/logo.png differ",
	}, "\n") + "\n"

	var out bytes.Buffer
	printUnifiedDiffStatColor(&out, []string{"/acme/app/new.go", "/acme/app/edit.go", "/acme/app/logo.png"}, diffText, false)
	got := out.String()
	for _, want := range []string{
		"3 changed path(s), +5 -1",
		"added     /acme/app/new.go    +3",
		"modified  /acme/app/edit.go   +2 -1",
		"modified  /acme/app/logo.png  binary",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diffstat missing %q in:\n%s", want, got)
		}
	}
}

func TestPrintChangesetDetailsDeduplicatesSinglePatchsetPaths(t *testing.T) {
	single := &corev1.Changeset{
		Id:                    "cs_aaaaaaaaaa",
		Status:                "draft",
		AuthoringSlice:        &corev1.SliceRef{Account: "acme", Slice: "app"},
		CurrentPatchsetId:     "ps_1",
		CurrentPatchsetNumber: 1,
		AffectedPaths:         []string{"/acme/app/a.go", "/acme/app/b.go"},
		Patchsets: []*corev1.Patchset{
			{Id: "ps_1", Number: 1, ChangedPaths: []string{"/acme/app/a.go", "/acme/app/b.go"}},
		},
	}
	var out bytes.Buffer
	printChangesetDetails(&out, single, false)
	if strings.Contains(out.String(), "affected_paths:") {
		t.Fatalf("single identical patchset should omit duplicate affected_paths:\n%s", out.String())
	}
	if strings.Count(out.String(), "/acme/app/a.go") != 1 {
		t.Fatalf("expected /acme/app/a.go once, got:\n%s", out.String())
	}

	multi := &corev1.Changeset{
		Id:                    "cs_bbbbbbbbbb",
		Status:                "draft",
		AuthoringSlice:        &corev1.SliceRef{Account: "acme", Slice: "app"},
		CurrentPatchsetId:     "ps_2",
		CurrentPatchsetNumber: 2,
		AffectedPaths:         []string{"/acme/app/a.go", "/acme/app/b.go"},
		Patchsets: []*corev1.Patchset{
			{Id: "ps_1", Number: 1, ChangedPaths: []string{"/acme/app/a.go"}},
			{Id: "ps_2", Number: 2, ChangedPaths: []string{"/acme/app/a.go", "/acme/app/b.go"}},
		},
	}
	out.Reset()
	printChangesetDetails(&out, multi, false)
	if !strings.Contains(out.String(), "affected_paths:") {
		t.Fatalf("multi-patchset changeset should keep affected_paths:\n%s", out.String())
	}
}

func TestResolveCheckCommandArgsAcceptsPositionalAndFlagForms(t *testing.T) {
	cs, target, err := resolveCheckCommandArgs("", []string{"e2e"}, "usage")
	if err != nil || cs != "" || target != "e2e" {
		t.Fatalf("1 arg = (%q, %q, %v)", cs, target, err)
	}
	cs, target, err = resolveCheckCommandArgs("3e00809fa1", []string{"e2e"}, "usage")
	if err != nil || cs != "3e00809fa1" || target != "e2e" {
		t.Fatalf("1 arg + flag = (%q, %q, %v)", cs, target, err)
	}
	cs, target, err = resolveCheckCommandArgs("", []string{"3e00809fa1", "gitslice/web/build"}, "usage")
	if err != nil || cs != "3e00809fa1" || target != "gitslice/web/build" {
		t.Fatalf("2 args = (%q, %q, %v)", cs, target, err)
	}
	if _, _, err := resolveCheckCommandArgs("3e00809fa1", []string{"3e00809fa1", "e2e"}, "usage"); !isUserErrorCode(err, "invalid_args") {
		t.Fatalf("expected invalid_args when both flag and positional cs given, got: %v", err)
	}
}

type fakeDiffChangesetServer struct {
	corev1.UnimplementedRepositoryServiceServer
	corev1.UnimplementedChangesetServiceServer
	lastDiffReq *corev1.DiffChangesetRequest
}

func (f *fakeDiffChangesetServer) GetCommit(context.Context, *corev1.GetCommitRequest) (*corev1.Commit, error) {
	return nil, status.Error(codes.NotFound, "commit not found")
}

func (f *fakeDiffChangesetServer) ResolveCommit(context.Context, *corev1.ResolveCommitRequest) (*corev1.ResolveCommitResponse, error) {
	return nil, status.Error(codes.NotFound, "commit not found")
}

func (f *fakeDiffChangesetServer) ListCommits(context.Context, *corev1.ListCommitsRequest) (*corev1.ListCommitsResponse, error) {
	return &corev1.ListCommitsResponse{}, nil
}

func (f *fakeDiffChangesetServer) DiffChangeset(_ context.Context, req *corev1.DiffChangesetRequest) (*corev1.DiffChangesetResponse, error) {
	f.lastDiffReq = req
	return &corev1.DiffChangesetResponse{
		ChangesetId:  req.ChangesetId,
		ToPatchsetId: "ps_1",
		ChangedPaths: []string{"/acme/app/main.go"},
		Diff:         "diff --git a/acme/app/main.go b/acme/app/main.go\n--- a/acme/app/main.go\n+++ b/acme/app/main.go\n@@ -1 +1,2 @@\n-old\n+new\n+more\n",
	}, nil
}

func TestDiffAndShowFallbackToChangesetWithStatAndPatch(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	fake := &fakeDiffChangesetServer{}
	corev1.RegisterRepositoryServiceServer(srv, fake)
	corev1.RegisterChangesetServiceServer(srv, fake)
	go srv.Serve(lis)
	defer srv.Stop()

	cfgDir := t.TempDir()
	r := Runner{
		Home: cfgDir,
		Dir:  cfgDir,
		Env:  map[string]string{},
	}
	if err := r.writeUserConfig(UserConfig{ServerAddr: lis.Addr().String(), Token: "tok", ActiveAccount: "acme"}); err != nil {
		t.Fatal(err)
	}

	// 1. gs diff <cs-handle> --stat falls back from commit-not-found to changeset diff with line counts.
	var stdout, stderr bytes.Buffer
	r.Stdout, r.Stderr = &stdout, &stderr
	if err := r.Run(context.Background(), []string{"diff", "51bb0c192c", "--stat"}); err != nil {
		t.Fatalf("gs diff <cs> --stat failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 changed path(s), +2 -1") || !strings.Contains(stdout.String(), "/acme/app/main.go") {
		t.Fatalf("unexpected gs diff <cs> --stat output:\n%s", stdout.String())
	}

	// 2. gs show --stat <cs-handle> renders diffstat instead of ignoring --stat.
	stdout.Reset()
	stderr.Reset()
	if err := r.Run(context.Background(), []string{"show", "--stat", "51bb0c192c"}); err != nil {
		t.Fatalf("gs show --stat <cs> failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 changed path(s), +2 -1") {
		t.Fatalf("unexpected gs show --stat <cs> output:\n%s", stdout.String())
	}

	// 3. gs show -p <cs-handle> renders patch.
	stdout.Reset()
	stderr.Reset()
	if err := r.Run(context.Background(), []string{"show", "-p", "51bb0c192c"}); err != nil {
		t.Fatalf("gs show -p <cs> failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "+more") {
		t.Fatalf("unexpected gs show -p <cs> output:\n%s", stdout.String())
	}

	// 4. gs log -n 5 parses cleanly.
	stdout.Reset()
	stderr.Reset()
	if err := r.Run(context.Background(), []string{"log", "-n", "5", "--all"}); err != nil {
		t.Fatalf("gs log -n 5 failed: %v\nstderr: %s", err, stderr.String())
	}
}
