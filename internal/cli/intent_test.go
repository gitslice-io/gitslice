package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestIntentCLIDeclareListOverlapAndClear(t *testing.T) {
	ctx := context.Background()
	const (
		aliceToken = "alice-intent-token"
		bobToken   = "bob-intent-token"
	)
	staticTokens := map[string]string{}
	serverAddr, mem := startAgentSignupServerWithStores(t, false, staticTokens)

	aliceID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "alice_int", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	staticTokens[aliceToken] = aliceID
	if _, err := mem.Auth.ChooseUsername(ctx, aliceID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.CreateOrganization(ctx, "acme", []string{aliceID}, aliceID); err != nil {
		t.Fatal(err)
	}

	bobID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "bob_int", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	staticTokens[bobToken] = bobID
	if _, err := mem.Auth.ChooseUsername(ctx, bobID, "bobby"); err != nil {
		t.Fatal(err)
	}

	data := []byte("package api\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(objectstore.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_cli_int", []storage.FileEntry{{
		Path:        "/acme/api/src/auth/login.go",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/api/src/auth/login.go"})

	slice, err := mem.Slices.Create(ctx, aliceID, &corev1.SliceRef{Account: "acme", Slice: "api"}, []string{"/acme/api"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetMember(ctx, slice.GetId(), bobID, "contributor", aliceID); err != nil {
		t.Fatal(err)
	}

	aliceRunner := Runner{Home: t.TempDir()}
	if err := aliceRunner.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: aliceToken, SubjectID: aliceID}); err != nil {
		t.Fatal(err)
	}
	bobRunner := Runner{Home: t.TempDir()}
	if err := bobRunner.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: bobToken, SubjectID: bobID}); err != nil {
		t.Fatal(err)
	}

	runAs := func(base Runner, dir string, args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		r := base
		r.Dir = dir
		r.Stdout, r.Stderr = &stdout, &stderr
		err := r.Run(ctx, args)
		if err != nil {
			return stdout.String() + stderr.String(), err
		}
		return stdout.String(), nil
	}

	// 1. Alice declares intent outside workspace using '<account>/<slice>/<glob>' and --json.
	out, err := runAs(aliceRunner, t.TempDir(), "intent", "acme/api/src/auth/**", "--for", "2h", "-m", "refactor auth", "--json")
	if err != nil {
		t.Fatalf("alice gs intent: %v\n%s", err, out)
	}
	var created pathIntentOutput
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("unmarshal intent JSON: %v\n%s", err, out)
	}
	if created.Pattern != "/acme/api/src/auth/**" || created.Slice != "acme:api" || created.Username != "alice" || created.Message != "refactor auth" {
		t.Fatalf("unexpected created intent: %+v", created)
	}

	// 2. Bob declares overlapping intent from inside a slice workspace (flags before positional arg) and gets an overlap warning!
	bobWS := t.TempDir()
	bR := bobRunner
	bR.Dir = bobWS
	if err := bR.writeWorkspaceConfig(WorkspaceConfig{
		Account:       "acme",
		Slice:         "api",
		SliceID:       slice.GetId(),
		IncludedPaths: []string{"/acme/api"},
		BaseCommitID:  "commit_cli_int",
	}); err != nil {
		t.Fatal(err)
	}
	if err := bR.writeWorkspaceState(WorkspaceState{}); err != nil {
		t.Fatal(err)
	}

	bobOut, err := runAs(bobRunner, bobWS, "intent", "--for", "30m", "-m", "touch login", "src/auth/login.go")
	if err != nil {
		t.Fatalf("bob gs intent inside workspace: %v\n%s", err, bobOut)
	}
	if !strings.Contains(bobOut, "declared intent ") || !strings.Contains(bobOut, "warning:") || !strings.Contains(bobOut, "alice") || !strings.Contains(bobOut, "refactor auth") {
		t.Fatalf("expected overlap warning in bob's gs intent output, got:\n%s", bobOut)
	}

	// 3. List intents via 'gs intent ls' inside workspace.
	lsOut, err := runAs(bobRunner, bobWS, "intent", "ls")
	if err != nil {
		t.Fatalf("gs intent ls: %v\n%s", err, lsOut)
	}
	if !strings.Contains(lsOut, created.ID) || !strings.Contains(lsOut, "/acme/api/src/auth/login.go") {
		t.Fatalf("unexpected gs intent ls output:\n%s", lsOut)
	}

	// 4. Bob clears by pattern ('gs intent clear src/auth/**') — clears ONLY Bob's intent, leaving Alice's intact!
	clearBob, err := runAs(bobRunner, bobWS, "intent", "clear", "src/auth/**", "--json")
	if err != nil {
		t.Fatalf("bob gs intent clear pattern: %v\n%s", err, clearBob)
	}
	var clearRes struct {
		Deleted    int      `json:"deleted"`
		DeletedIDs []string `json:"deleted_ids"`
	}
	if err := json.Unmarshal([]byte(clearBob), &clearRes); err != nil || clearRes.Deleted != 1 {
		t.Fatalf("unexpected clear JSON: %s (%v)", clearBob, err)
	}

	// Alice's intent still present; Alice clears with --all.
	clearAlice, err := runAs(aliceRunner, t.TempDir(), "intent", "clear", "--slice", "acme/api", "--all")
	if err != nil || !strings.Contains(clearAlice, created.ID) {
		t.Fatalf("alice gs intent clear --all: %v\n%s", err, clearAlice)
	}
	emptyOut, err := runAs(bobRunner, bobWS, "intent", "list")
	if err != nil || !strings.Contains(emptyOut, "no active path intents") {
		t.Fatalf("expected empty list after clear, got: %v\n%s", err, emptyOut)
	}
}
