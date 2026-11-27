package service

import (
	"context"
	"slices"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDaemonEventOwnershipAndPersonalAccountAttribution(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	alice := subjectCtx("user_alice")
	bob := subjectCtx("user_bob")

	// Bob is a reader on "acme" (alphabetically before "bob"), so accounts[0] would be "acme".
	// Connect and StandbyPoll must attribute Bob's daemon to "bob", never hijacking "acme"!
	mem.AddAccountRole("user_bob", "acme", "reader")

	aliceDaemonID, closeAlice := connectTestDaemon(t, alice, handlers, "shared-name", "codex")
	defer closeAlice()
	bobDaemonID, closeBob := connectTestDaemon(t, bob, handlers, "shared-name", "codex")
	defer closeBob()

	if aliceDaemonID == bobDaemonID {
		t.Fatalf("bob hijacked alice's daemon id %q via org membership", aliceDaemonID)
	}
	bobDaemon, err := mem.Agents.GetDaemon(bob, bobDaemonID)
	if err != nil {
		t.Fatal(err)
	}
	if bobDaemon.GetAccount() != "bob" {
		t.Fatalf("bob daemon account = %q, want personal account bob", bobDaemon.GetAccount())
	}

	standby, err := handlers.Agent.StandbyPoll(bob, &corev1.StandbyPollRequest{Name: "standby-ci", Runtime: ciDaemonRuntime})
	if err != nil {
		t.Fatal(err)
	}
	standbyDaemon, err := mem.Agents.GetDaemon(bob, standby.GetDaemonId())
	if err != nil {
		t.Fatal(err)
	}
	if standbyDaemon.GetAccount() != "bob" {
		t.Fatalf("bob standby daemon account = %q, want bob", standbyDaemon.GetAccount())
	}

	// Non-admin org member with no personal account cannot register a daemon in the org.
	mem.AddAccountRole("user_eve", "acme", "reader")
	eve := subjectCtx("user_eve")
	if _, err := handlers.Agent.StandbyPoll(eve, &corev1.StandbyPollRequest{Name: "hijack-ci", Runtime: ciDaemonRuntime}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("StandbyPoll by non-admin without personal account = %v, want PermissionDenied", err)
	}

	// Daemon B must not append events to Daemon A's conversation.
	conv, err := handlers.Agent.CreateConversation(alice, &corev1.CreateConversationRequest{
		DaemonId: aliceDaemonID,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Title:    "alice conv",
	})
	if err != nil {
		t.Fatal(err)
	}

	rogueStream := newFakeAgentConnectStream(bob)
	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(rogueStream) }()
	rogueStream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:    "rogue",
		Runtime: "codex",
	}}}
	<-rogueStream.sent
	rogueStream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: &corev1.AgentEvent{
		ConversationId: conv.Id,
		Role:           "agent",
		Type:           "message",
		Text:           "injected by rogue daemon",
		ClientSeq:      1,
	}}}
	close(rogueStream.recv)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	events, err := mem.Agents.ListEvents(alice, conv.Id, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.GetText() == "injected by rogue daemon" {
			t.Fatalf("rogue daemon injected event into another daemon's conversation: %#v", ev)
		}
	}
}

func TestRerunCheckRequiresWriteAccess(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	mem.AddAccountRole("user_carol", "acme", "reader")
	alice := subjectCtx("user_alice")
	carol := subjectCtx("user_carol")

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	ref, err := handlers.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, ps := createStandaloneChangesetWithPatchsetForSlice(t, alice, handlers, sliceRef, ref.CommitId, "check rerun", "/acme/payment/x.go", "package payment\n")
	run, err := mem.Checks.CreateCheckRun(alice, storage.CheckRunInput{
		ChangesetID: cs.Id,
		PatchsetID:  ps.Id,
		CheckName:   "unit",
		Provenance:  "ci",
		Status:      "failed",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := handlers.Check.RerunCheck(carol, &corev1.RerunCheckRequest{RunId: run.Id}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("RerunCheck by reader error = %v, want PermissionDenied", err)
	}
}

type blameMoveTestRepo struct {
	storage.RepositoryStore
	commits []*corev1.Commit
	changes []storage.FileChange
}

func (r *blameMoveTestRepo) ListCommitPageByEntityRefsOrPathPrefixes(ctx context.Context, refName string, entityRefs []storage.HistoryEntityRef, prefixes []string, limit int, pageToken string) (*storage.CommitListPage, error) {
	return &storage.CommitListPage{Commits: r.commits}, nil
}

func (r *blameMoveTestRepo) ListCommitPageByEntityRefs(ctx context.Context, refName string, entityRefs []storage.HistoryEntityRef, limit int, pageToken string) (*storage.CommitListPage, error) {
	return &storage.CommitListPage{Commits: r.commits}, nil
}

func (r *blameMoveTestRepo) ListCommitPageByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int, pageToken string) (*storage.CommitListPage, error) {
	return &storage.CommitListPage{Commits: r.commits}, nil
}

func (r *blameMoveTestRepo) FileChanges(ctx context.Context, refName string, commitIDs []string) ([]storage.FileChange, error) {
	return r.changes, nil
}

func TestBlameRedactsUnreadableMovesAndPrivateChangedPaths(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	handlers.Repository.Blames = mem.Blames
	alice := subjectCtx("user_alice")
	outsider := subjectCtx("user_outsider")

	privContent := []byte("line1\nline2\n")
	pubContent := []byte("line1\nline2\nline3\n")
	privSideContent := []byte("top-secret\n")
	for _, data := range [][]byte{privContent, pubContent, privSideContent} {
		mem.PutObject(objectstore.BlobKey(objectid.RawContentHash(data)), data)
	}

	// Commit 1 creates /acme/secret/orig.txt in a private slice.
	mem.PutCommitWithFiles("c1_private", []storage.FileEntry{{
		Path:        "/acme/secret/orig.txt",
		BlobID:      objectid.BlobID(privContent),
		ContentHash: objectid.RawContentHash(privContent),
		Mode:        0o100644,
		Size:        int64(len(privContent)),
	}}, []string{"/acme/secret/orig.txt"})

	// Commit 2 moves /acme/secret/orig.txt -> /acme/docs/pub.txt (public slice) AND modifies /acme/secret/side.txt.
	mem.PutCommitWithFiles("c2_move", []storage.FileEntry{
		{
			Path:        "/acme/docs/pub.txt",
			BlobID:      objectid.BlobID(pubContent),
			ContentHash: objectid.RawContentHash(pubContent),
			Mode:        0o100644,
			Size:        int64(len(pubContent)),
		},
		{
			Path:        "/acme/secret/side.txt",
			BlobID:      objectid.BlobID(privSideContent),
			ContentHash: objectid.RawContentHash(privSideContent),
			Mode:        0o100644,
			Size:        int64(len(privSideContent)),
		},
	}, []string{"/acme/secret/orig.txt", "/acme/docs/pub.txt", "/acme/secret/side.txt"})

	c1, err := mem.Repository.GetCommit(context.Background(), "c1_private")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := mem.Repository.GetCommit(context.Background(), "c2_move")
	if err != nil {
		t.Fatal(err)
	}
	handlers.Repository.Repository = &blameMoveTestRepo{
		RepositoryStore: mem.Repository,
		commits:         []*corev1.Commit{c2, c1},
		changes: []storage.FileChange{
			{
				CommitID:    "c2_move",
				Path:        "/acme/docs/pub.txt",
				OldPath:     "/acme/secret/orig.txt",
				ChangeKind:  "moved",
				ContentHash: objectid.RawContentHash(pubContent),
			},
			{
				CommitID:    "c1_private",
				Path:        "/acme/secret/orig.txt",
				ChangeKind:  "added",
				ContentHash: objectid.RawContentHash(privContent),
			},
		},
	}

	// Warm snapshot as alice (who CAN read /acme/secret) first, then verify outsider still gets no leak!
	aliceBlame, err := handlers.Repository.BlameFile(alice, &corev1.BlameFileRequest{Path: "/acme/docs/pub.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceBlame.GetCommits()) < 2 {
		t.Fatalf("alice blame commits = %d, want both c1_private and c2_move", len(aliceBlame.GetCommits()))
	}

	for _, useIndex := range []bool{true, false} {
		SetBlameFromIndex(useIndex)
		resp, err := handlers.Repository.BlameFile(outsider, &corev1.BlameFileRequest{
			Path:  "/acme/docs/pub.txt",
			Slice: &corev1.SliceRef{Account: "acme", Slice: "docs"},
		})
		if err != nil {
			SetBlameFromIndex(true)
			t.Fatalf("BlameFile(index=%v): %v", useIndex, err)
		}
		for _, c := range resp.GetCommits() {
			if c.GetId() == "c1_private" {
				SetBlameFromIndex(true)
				t.Fatalf("BlameFile(index=%v) leaked private pre-move commit c1_private", useIndex)
			}
			for _, changed := range c.GetChangedPaths() {
				if slices.Contains([]string{"/acme/secret/orig.txt", "/acme/secret/side.txt"}, changed) {
					SetBlameFromIndex(true)
					t.Fatalf("BlameFile(index=%v) leaked private ChangedPath %q in commit %s", useIndex, changed, c.GetId())
				}
			}
		}
	}
	SetBlameFromIndex(true)
}

func TestRepositoryRefEmptyCommitAndRootTreeAuthorization(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	alice := subjectCtx("user_alice")
	bob := subjectCtx("user_bob") // personal account "bob", NOT a member of "acme"
	outsider := subjectCtx("user_outsider")

	// 1. GetRef rejects non-default ref names.
	if _, err := handlers.Repository.GetRef(alice, &corev1.GetRefRequest{RefName: "refs/internal/secret"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("GetRef non-default ref error = %v, want InvalidArgument", err)
	}

	// 2. Empty commit authz: outsider with 0 accounts cannot read genesis empty commit ("mem_root").
	if _, err := handlers.Repository.GetCommit(outsider, &corev1.GetCommitRequest{CommitId: "mem_root"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetCommit empty commit by outsider = %v, want PermissionDenied", err)
	}
	if _, err := handlers.Repository.GetCommit(alice, &corev1.GetCommitRequest{CommitId: "mem_root"}); err != nil {
		t.Fatalf("GetCommit empty commit by account member = %v", err)
	}

	// 3. RootTreeId verification: private draft changeset tree cannot be read through public path by non-member,
	// and forged/non-existent RootTreeId is rejected.
	docData := []byte("public doc\n")
	mem.PutObject(objectstore.BlobKey(objectid.RawContentHash(docData)), docData)
	mem.PutCommitWithFiles("c_pub_seed", []storage.FileEntry{{
		Path:        "/acme/docs/readme.md",
		BlobID:      objectid.BlobID(docData),
		ContentHash: objectid.RawContentHash(docData),
		Mode:        0o100644,
		Size:        int64(len(docData)),
	}}, []string{"/acme/docs/readme.md"})

	ref, err := handlers.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, privatePS := createStandaloneChangesetWithPatchsetForSlice(t, alice, handlers, &corev1.SliceRef{Account: "acme", Slice: "secret"}, ref.CommitId, "secret draft", "/acme/secret/key.txt", "secret-key\n")

	pubSlice := &corev1.SliceRef{Account: "acme", Slice: "docs"}
	if _, err := handlers.Repository.ReadFile(bob, &corev1.ReadFileRequest{
		RootTreeId: privatePS.ResultTreeId,
		Path:       "/acme/docs/readme.md",
		Slice:      pubSlice,
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ReadFile with unauthorized private draft RootTreeId = %v, want PermissionDenied", err)
	}
	if _, err := handlers.Repository.ReadFiles(bob, &corev1.ReadFilesRequest{
		RootTreeId: privatePS.ResultTreeId,
		Paths:      []string{"/acme/docs/readme.md"},
		Slice:      pubSlice,
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ReadFiles with unauthorized private draft RootTreeId = %v, want PermissionDenied", err)
	}
	if _, err := handlers.Repository.ResolvePath(bob, &corev1.ResolvePathRequest{
		RootTreeId: privatePS.ResultTreeId,
		Path:       "/acme/docs/readme.md",
		Slice:      pubSlice,
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ResolvePath with unauthorized private draft RootTreeId = %v, want PermissionDenied", err)
	}
	if _, err := handlers.Repository.ListDirectory(bob, &corev1.ListDirectoryRequest{
		RootTreeId: privatePS.ResultTreeId,
		Path:       "/acme/docs",
		Slice:      pubSlice,
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ListDirectory with unauthorized private draft RootTreeId = %v, want PermissionDenied", err)
	}
	if err := handlers.Repository.Checkout(&corev1.CheckoutRequest{
		RootTreeId: privatePS.ResultTreeId,
		Paths:      []string{"/acme/docs"},
	}, &checkoutStream{ctx: bob}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Checkout with unauthorized private draft RootTreeId = %v, want PermissionDenied", err)
	}

	// Forged RootTreeId returns NotFound even for authorized member.
	if _, err := handlers.Repository.ReadFile(alice, &corev1.ReadFileRequest{
		RootTreeId: "tree_forged_not_published",
		Path:       "/acme/docs/readme.md",
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("ReadFile with forged RootTreeId = %v, want NotFound", err)
	}

	// Authorized member CAN read their own changeset preview RootTreeId.
	if _, err := handlers.Repository.ReadFile(alice, &corev1.ReadFileRequest{
		RootTreeId: privatePS.ResultTreeId,
		Path:       "/acme/secret/key.txt",
	}); err != nil {
		t.Fatalf("ReadFile with authorized changeset RootTreeId failed: %v", err)
	}

	// Anonymous viewer CAN read a draft changeset's ResultTreeId authored in a public slice.
	_, pubPS := createStandaloneChangesetWithPatchsetForSlice(t, alice, handlers, pubSlice, ref.CommitId, "public draft", "/acme/docs/draft.md", "# Draft\n")
	if got, err := handlers.Repository.ReadFile(context.Background(), &corev1.ReadFileRequest{
		RootTreeId: pubPS.ResultTreeId,
		Path:       "/acme/docs/draft.md",
		Slice:      pubSlice,
	}); err != nil {
		t.Fatalf("anonymous ReadFile with public draft RootTreeId failed: %v", err)
	} else if string(got.GetData()) != "# Draft\n" {
		t.Fatalf("anonymous ReadFile data = %q, want %q", string(got.GetData()), "# Draft\n")
	}
}

type failScanRepositoryStore struct {
	storage.RepositoryStore
	t *testing.T
}

func (s failScanRepositoryStore) ListCommitPage(ctx context.Context, refName string, limit int, pageToken string) (*storage.CommitListPage, error) {
	s.t.Helper()
	s.t.Fatal("ListCommitPage should not be called during root_tree_id authorization")
	return nil, nil
}

type failScanChangesetStore struct {
	storage.ChangesetStore
	t *testing.T
}

func (s failScanChangesetStore) List(ctx context.Context, filter storage.ChangesetListFilter) ([]*corev1.Changeset, string, error) {
	s.t.Helper()
	s.t.Fatal("Changesets.List should not be called during root_tree_id authorization")
	return nil, "", nil
}

func TestReadFileDraftRootTreeUsesDirectLookupsWithoutScanning(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	alice := subjectCtx("user_alice")
	pubSlice := &corev1.SliceRef{Account: "acme", Slice: "docs"}

	ref, err := handlers.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, ps := createStandaloneChangesetWithPatchsetForSlice(t, alice, handlers, pubSlice, ref.CommitId, "add doc", "/acme/docs/new.md", "hello\n")

	handlers.Repository.Repository = failScanRepositoryStore{RepositoryStore: mem.Repository, t: t}
	handlers.Repository.Changesets = failScanChangesetStore{ChangesetStore: mem.Changesets, t: t}

	got, err := handlers.Repository.ReadFile(context.Background(), &corev1.ReadFileRequest{
		RootTreeId: ps.ResultTreeId,
		Path:       "/acme/docs/new.md",
		Slice:      pubSlice,
	})
	if err != nil {
		t.Fatalf("ReadFile on draft ResultTreeId failed: %v", err)
	}
	if string(got.GetData()) != "hello\n" {
		t.Fatalf("ReadFile data = %q, want %q", string(got.GetData()), "hello\n")
	}

	if _, err := handlers.Repository.ReadFile(context.Background(), &corev1.ReadFileRequest{
		RootTreeId: "sha256:does-not-exist",
		Path:       "/acme/docs/new.md",
		Slice:      pubSlice,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("ReadFile on unknown RootTreeId = %v, want NotFound", err)
	}
}
