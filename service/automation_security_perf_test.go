package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newAutomationTestServices(mem *memory.Stores, dispatchSecret string) (*automations.Engine, *AutomationService, *AutomationRunService) {
	tree := TreeFiles{Repository: mem.Repository, Objects: mem.Objects}
	engine := automations.NewEngine(automations.Stores{
		Automations: mem.Automations,
		Changesets:  mem.Changesets,
		Slices:      mem.Slices,
		Repository:  mem.Repository,
		Checks:      mem.Checks,
	}, automations.Options{
		Tree:                tree,
		DispatchURL:         "https://automations.test",
		DispatchSecret:      dispatchSecret,
		AllowPrivateTargets: true,
		Templates: &sliceTemplateLookup{
			Repository: mem.Repository,
			Slices:     mem.Slices,
			Auth:       mem.Auth,
		},
	})
	trigSvc := &AutomationService{
		Auth:        mem.Auth,
		Changesets:  mem.Changesets,
		Slices:      mem.Slices,
		Repository:  mem.Repository,
		Automations: mem.Automations,
		Tree:        tree,
		Engine:      engine,
	}
	runSvc := &AutomationRunService{
		Changesets:  mem.Changesets,
		Slices:      mem.Slices,
		Repository:  mem.Repository,
		Checks:      mem.Checks,
		Automations: mem.Automations,
		Tree:        tree,
		Engine:      engine,
	}
	return engine, trigSvc, runSvc
}

func TestTriggerServiceAuthorizationAndRefGuards(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	_, trigSvc, _ := newAutomationTestServices(mem, "test-dispatch-secret")

	// Add a public slice and a private secret-ops slice under acme.
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "public-docs"}, []string{"/acme/public"}, "public")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "secret-ops"}, []string{"/acme/secret-ops"}, "private")
	mem.AddPersonalAccount("user_reader", "reader")
	mem.AddPersonalAccount("user_ops_writer", "opswriter")
	mem.AddPersonalAccount("user_outsider", "outsider")

	alice := subjectCtx("user_alice")
	bob := subjectCtx("user_bob")
	reader := subjectCtx("user_reader")
	opsWriter := subjectCtx("user_ops_writer")
	outsider := subjectCtx("user_outsider")
	anon := context.Background()

	if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "public-docs"},
		Username: "bob",
		Role:     "writer",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "public-docs"},
		Username: "reader",
		Role:     "reader",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "secret-ops"},
		Username: "opswriter",
		Role:     "writer",
	}); err != nil {
		t.Fatal(err)
	}

	// 1. ListAutomations requires authentication and restricts ref to default target ref.
	_, err := trigSvc.ListAutomations(anon, &corev1.ListAutomationsRequest{Path: "/acme/public"})
	wantCode(t, err, codes.Unauthenticated, "ListAutomations unauthenticated")

	_, err = trigSvc.ListAutomations(alice, &corev1.ListAutomationsRequest{Path: "/acme/public", Ref: "refs/heads/secret-branch"})
	wantCode(t, err, codes.InvalidArgument, "ListAutomations non-default ref")

	if _, err := trigSvc.ListAutomations(alice, &corev1.ListAutomationsRequest{Path: "/acme/public", Ref: "refs/global/main"}); err != nil {
		t.Fatalf("ListAutomations default ref for admin: %v", err)
	}

	// Create a changeset on pubSlice, and a trigger run whose Folder is /acme/secret-ops.
	head, err := h.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := h.Changeset.CreateChangeset(bob, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "public-docs"},
		BaseCommitId:   head.CommitId,
		Title:          "public doc edit",
	})
	if err != nil {
		t.Fatal(err)
	}

	specBytes, _ := json.Marshal(automations.RunSpec{
		Name: "audit",
		Automation: automations.Automation{
			On:      []string{"changeset.opened"},
			Timeout: time.Minute,
			Action:  automations.Action{Kind: automations.ActionWorker, Worker: "audit.js"},
		},
	})
	inserted, err := mem.Automations.CreateAutomationRuns(context.Background(), []storage.AutomationRun{{
		ID:                 "trun_secret_folder",
		Account:            "acme",
		AutomationID:       "secret-ops:audit",
		Folder:             "/acme/secret-ops",
		Action:             automations.ActionWorker,
		EventID:            "ev_1",
		EventKind:          "changeset.opened",
		ChangesetID:        cs.Id,
		DefinitionCommitID: head.CommitId,
		Spec:               specBytes,
		Payload:            []byte(`{}`),
		Status:             storage.AutomationRunRunning,
		MaxAttempts:        1,
		CreatedAt:          time.Now().UTC(),
	}})
	if err != nil || len(inserted) != 1 {
		t.Fatalf("CreateAutomationRuns: %v (len=%d)", err, len(inserted))
	}
	if _, err := mem.Automations.AppendAutomationRunLog(context.Background(), "trun_secret_folder", "stdout", "internal log line"); err != nil {
		t.Fatal(err)
	}

	// 2. ListAutomationRunLogs requires auth AND read access covering run.Folder (/acme/secret-ops).
	_, err = trigSvc.ListAutomationRunLogs(anon, &corev1.ListAutomationRunLogsRequest{RunId: "trun_secret_folder"})
	wantCode(t, err, codes.Unauthenticated, "ListAutomationRunLogs unauthenticated on public changeset")

	_, err = trigSvc.ListAutomationRunLogs(outsider, &corev1.ListAutomationRunLogsRequest{RunId: "trun_secret_folder"})
	if status.Code(err) != codes.NotFound && status.Code(err) != codes.PermissionDenied {
		t.Fatalf("outsider ListAutomationRunLogs error = %v, want NotFound or PermissionDenied", err)
	}

	// opsWriter covers /acme/secret-ops and pubSlice is public (ActionRead passes on changeset).
	logsResp, err := trigSvc.ListAutomationRunLogs(opsWriter, &corev1.ListAutomationRunLogsRequest{RunId: "trun_secret_folder"})
	if err != nil || len(logsResp.GetLogs()) != 1 {
		t.Fatalf("opsWriter ListAutomationRunLogs = %+v, %v", logsResp, err)
	}

	// 3. CancelAutomationRun & RerunAutomationRun require auth + ActionWrite on both changeset and run.Folder.
	_, err = trigSvc.CancelAutomationRun(anon, &corev1.CancelAutomationRunRequest{RunId: "trun_secret_folder"})
	wantCode(t, err, codes.Unauthenticated, "CancelAutomationRun unauthenticated")

	_, err = trigSvc.CancelAutomationRun(reader, &corev1.CancelAutomationRunRequest{RunId: "trun_secret_folder"})
	wantCode(t, err, codes.PermissionDenied, "CancelAutomationRun reader")

	// bob can write pubSlice (the changeset's authoring slice) but NOT /acme/secret-ops!
	_, err = trigSvc.CancelAutomationRun(bob, &corev1.CancelAutomationRunRequest{RunId: "trun_secret_folder"})
	if status.Code(err) != codes.NotFound && status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bob CancelAutomationRun without folder write access = %v, want NotFound or PermissionDenied", err)
	}
	_, err = trigSvc.RerunAutomationRun(bob, &corev1.RerunAutomationRunRequest{RunId: "trun_secret_folder"})
	if status.Code(err) != codes.NotFound && status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bob RerunAutomationRun without folder write access = %v, want NotFound or PermissionDenied", err)
	}

	// Account owner alice can cancel and rerun.
	if _, err := trigSvc.CancelAutomationRun(alice, &corev1.CancelAutomationRunRequest{RunId: "trun_secret_folder"}); err != nil {
		t.Fatalf("alice CancelAutomationRun: %v", err)
	}
	if _, err := trigSvc.RerunAutomationRun(alice, &corev1.RerunAutomationRunRequest{RunId: "trun_secret_folder"}); err != nil {
		t.Fatalf("alice RerunAutomationRun: %v", err)
	}
}

type mapTreeReader map[string]string

func (m mapTreeReader) ReadFile(_ context.Context, _, p string) ([]byte, error) {
	v, ok := m[p]
	if !ok {
		return nil, automations.ErrNotFound
	}
	return []byte(v), nil
}

func TestTriggerRunCodeHashCallbackClaimAndCommentStripping(t *testing.T) {
	// 1. Comment stripping in loadTriggerModules ignores commented-out imports while loading real ones.
	fakeTree := mapTreeReader{
		"/acme/app/.gitslice/main.js": `
// import "./missing_single_line.js"
/*
   import "./missing_block.js";
   from "../escape.js"
*/
import { helper } from "./helper";
export default async function() { return helper("/* not a comment */ // also string"); }
`,
		"/acme/app/.gitslice/helper.js": `export function helper(x) { return x; }`,
	}
	mods, err := loadTriggerModules(context.Background(), fakeTree, "tree_1", "/acme/app/.gitslice", "main.js")
	if err != nil {
		t.Fatalf("loadTriggerModules failed on commented imports: %v", err)
	}
	if len(mods) != 2 || mods["helper.js"] == "" {
		t.Fatalf("loaded modules = %v, want main.js and helper.js", mods)
	}

	// 2. CodeHash binds Account and Folder, and StartCallback atomically claims AutomationRunWaiting -> AutomationRunRunning.
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccount("user_other", "otherorg")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "ops"}, []string{"/acme/ops"}, "private")
	mem.PutSlice(&corev1.SliceRef{Account: "otherorg", Slice: "payment"}, []string{"/otherorg/payment"}, "private")

	const secret = "dispatch-secret-xyz"
	engine, _, runSvc := newAutomationTestServices(mem, secret)
	alice := subjectCtx("user_alice")
	otherOwner := subjectCtx("user_other")

	commitWorker := func(ctx context.Context, sliceRef *corev1.SliceRef, folder string) string {
		t.Helper()
		head, err := h.Repository.GetRef(ctx, &corev1.GetRefRequest{})
		if err != nil {
			t.Fatal(err)
		}
		script := []byte("export default async function() {}\n")
		up, err := h.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: script, Slice: sliceRef})
		if err != nil {
			t.Fatal(err)
		}
		cs, err := h.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
			AuthoringSlice: sliceRef,
			BaseCommitId:   head.CommitId,
			Title:          "add worker",
		})
		if err != nil {
			t.Fatal(err)
		}
		ps, err := h.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
			ChangesetId:  cs.Id,
			BaseCommitId: head.CommitId,
			FileEdits: []*corev1.FileEdit{{
				Op:          "upsert",
				Path:        folder + "/.gitslice/worker.js",
				BlobId:      up.BlobId,
				ContentHash: up.ContentHash,
				Mode:        0o100644,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
			ChangesetId:               cs.Id,
			ExpectedCurrentPatchsetId: ps.Id,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := mem.Changesets.PublishPending(ctx, 10); err != nil {
			t.Fatal(err)
		}
		after, err := h.Repository.GetRef(ctx, &corev1.GetRefRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return after.CommitId
	}

	c1 := commitWorker(alice, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "/acme/payment")
	c2 := commitWorker(alice, &corev1.SliceRef{Account: "acme", Slice: "ops"}, "/acme/ops")
	c3 := commitWorker(otherOwner, &corev1.SliceRef{Account: "otherorg", Slice: "payment"}, "/otherorg/payment")

	specBytes, _ := json.Marshal(automations.RunSpec{
		Name: "w",
		Automation: automations.Automation{
			On:      []string{"push"},
			Timeout: 2 * time.Minute,
			Action:  automations.Action{Kind: automations.ActionWorker, Worker: "worker.js"},
		},
	})
	now := time.Now().UTC()
	_, err = mem.Automations.CreateAutomationRuns(context.Background(), []storage.AutomationRun{
		{ID: "run_acme_pay", Account: "acme", AutomationID: "payment:w", Folder: "/acme/payment", Action: automations.ActionWorker, EventID: "e1", EventKind: "push", DefinitionCommitID: c1, Spec: specBytes, Payload: []byte(`{}`), Status: storage.AutomationRunQueued, MaxAttempts: 1, CreatedAt: now},
		{ID: "run_acme_ops", Account: "acme", AutomationID: "ops:w", Folder: "/acme/ops", Action: automations.ActionWorker, EventID: "e2", EventKind: "push", DefinitionCommitID: c2, Spec: specBytes, Payload: []byte(`{}`), Status: storage.AutomationRunQueued, MaxAttempts: 1, CreatedAt: now},
		{ID: "run_other_pay", Account: "otherorg", AutomationID: "payment:w", Folder: "/otherorg/payment", Action: automations.ActionWorker, EventID: "e3", EventKind: "push", DefinitionCommitID: c3, Spec: specBytes, Payload: []byte(`{}`), Status: storage.AutomationRunQueued, MaxAttempts: 1, CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	r1, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: "run_acme_pay", DispatcherKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: "run_acme_ops", DispatcherKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	r3, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: "run_other_pay", DispatcherKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	if r1.CodeHash == r2.CodeHash {
		t.Fatalf("CodeHash did not change across folders: %s", r1.CodeHash)
	}
	if r1.CodeHash == r3.CodeHash {
		t.Fatalf("CodeHash did not change across accounts: %s", r1.CodeHash)
	}

	// Transition run_acme_pay to waiting (as FinishRun(Wait=true) does) and race two StartCallback calls.
	if _, err := runSvc.FinishRun(context.Background(), &corev1.FinishAutomationRunRequest{
		RunToken: r1.RunToken,
		Outcome:  "succeeded",
		Wait:     true,
	}); err != nil {
		t.Fatal(err)
	}

	sig := engine.CallbackSignature("run_acme_pay")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	resps := make([]*corev1.StartAutomationRunResponse, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resps[idx], errs[idx] = runSvc.StartCallback(context.Background(), &corev1.StartAutomationRunRequest{
				RunId:             "run_acme_pay",
				DispatcherKey:     secret,
				CallbackSignature: sig,
			})
		}(i)
	}
	wg.Wait()

	okCount, conflictCount := 0, 0
	var cbToken string
	for i, cerr := range errs {
		if cerr == nil {
			okCount++
			cbToken = resps[i].GetRunToken()
		} else if status.Code(cerr) == codes.FailedPrecondition {
			conflictCount++
		} else {
			t.Fatalf("unexpected StartCallback error[%d]: %v", i, cerr)
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("concurrent StartCallback results: ok=%d conflict=%d, want 1 and 1", okCount, conflictCount)
	}

	// FinishRun(Finish=true) from the callback completes cleanly.
	finished, err := runSvc.FinishRun(context.Background(), &corev1.FinishAutomationRunRequest{
		RunToken: cbToken,
		Outcome:  "succeeded",
		Finish:   true,
	})
	if err != nil || finished.GetStatus() != storage.AutomationRunSucceeded {
		t.Fatalf("FinishRun after StartCallback = %+v, %v", finished, err)
	}
}

type discardObjectStore struct {
	ObjectStore
}

func (d discardObjectStore) Put(_ context.Context, _ string, r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

func (d discardObjectStore) Delete(_ context.Context, _ string) error {
	return nil
}

type repeatingUploadStream struct {
	grpc.ServerStream
	ctx       context.Context
	slice     *corev1.SliceRef
	chunk     []byte
	remaining int
	sentInit  bool
}

func (s *repeatingUploadStream) Context() context.Context { return s.ctx }

func (s *repeatingUploadStream) Recv() (*corev1.UploadBlobChunk, error) {
	if !s.sentInit {
		s.sentInit = true
		return &corev1.UploadBlobChunk{
			Payload: &corev1.UploadBlobChunk_Init{Init: &corev1.UploadBlobInit{Slice: s.slice}},
		}, nil
	}
	if s.remaining <= 0 {
		return nil, io.EOF
	}
	s.remaining--
	return &corev1.UploadBlobChunk{
		Payload: &corev1.UploadBlobChunk_Data{Data: s.chunk},
	}, nil
}

func (s *repeatingUploadStream) SendAndClose(*corev1.UploadBlobResponse) error { return nil }

type oversizedZeroStreamObjectStore struct {
	ObjectStore
	key  string
	size int64
}

func (o oversizedZeroStreamObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if key == o.key {
		rem := o.size - offset
		if length > 0 && length < rem {
			rem = length
		}
		return io.NopCloser(io.LimitReader(zeroReader{}, rem)), nil
	}
	return o.ObjectStore.Get(ctx, key, offset, length)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestBlobAndRepositoryResourceCaps(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	alice := subjectCtx("user_alice")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}

	// 1. GetBlobStatus caps ContentHashes at getBlobStatusMaxHashes (1000).
	tooMany := make([]string, getBlobStatusMaxHashes+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("sha256:%064x", i)
	}
	_, err := h.Blob.GetBlobStatus(alice, &corev1.GetBlobStatusRequest{
		Slice:         sliceRef,
		ContentHashes: tooMany,
	})
	wantCode(t, err, codes.InvalidArgument, "GetBlobStatus > 1000 hashes")

	// 2. UploadBlobStream rejects declared size > 256 MiB and streamed bytes > 256 MiB.
	blobSvc := &BlobService{
		Auth:        mem.Auth,
		Blobs:       mem.Blobs,
		Slices:      mem.Slices,
		ObjectStore: discardObjectStore{ObjectStore: mem.Objects},
	}
	tooBigDeclared := int64(maxBlobStreamUploadBytes + 1)
	err = blobSvc.UploadBlobStream(&uploadBlobStreamServerForTest{
		ctx: authctx.WithSubjectID(context.Background(), "user_alice"),
		chunks: []*corev1.UploadBlobChunk{{
			Payload: &corev1.UploadBlobChunk_Init{Init: &corev1.UploadBlobInit{
				Slice: sliceRef,
				Size:  &tooBigDeclared,
			}},
		}},
	})
	wantCode(t, err, codes.ResourceExhausted, "UploadBlobStream declared size > 256 MiB")

	oneMiB := make([]byte, blobStreamChunkBytes)
	err = blobSvc.UploadBlobStream(&repeatingUploadStream{
		ctx:       authctx.WithSubjectID(context.Background(), "user_alice"),
		slice:     sliceRef,
		chunk:     oneMiB,
		remaining: (maxBlobStreamUploadBytes / blobStreamChunkBytes) + 1,
	})
	wantCode(t, err, codes.ResourceExhausted, "UploadBlobStream streamed > 256 MiB")

	// 3. Unary ReadFile rejects length > 64 MiB and streams > 64 MiB when req.Length == 0.
	head, err := h.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	small, err := h.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{Data: []byte("hello\n"), Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	csRead, err := h.Changeset.CreateChangeset(alice, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		BaseCommitId:   head.CommitId,
		Title:          "seed file",
	})
	if err != nil {
		t.Fatal(err)
	}
	psRead, err := h.Changeset.UpdateChangeset(alice, &corev1.UpdateChangesetRequest{
		ChangesetId:  csRead.Id,
		BaseCommitId: head.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/seed.txt",
			BlobId:      small.BlobId,
			ContentHash: small.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{
		ChangesetId:               csRead.Id,
		ExpectedCurrentPatchsetId: psRead.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Changesets.PublishPending(alice, 10); err != nil {
		t.Fatal(err)
	}
	landed, err := h.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.Repository.ReadFile(alice, &corev1.ReadFileRequest{
		Slice:    sliceRef,
		CommitId: landed.CommitId,
		Path:     "/acme/payment/seed.txt",
		Length:   maxUnaryReadFileBytes + 1,
	})
	wantCode(t, err, codes.ResourceExhausted, "ReadFile explicit length > 64 MiB")

	// Even if metadata Size is small, LimitReader caps unary ReadFile (req.Length == 0) at 64 MiB.
	repoBounded := &RepositoryService{
		Auth:       mem.Auth,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Changesets: mem.Changesets,
		ObjectStore: oversizedZeroStreamObjectStore{
			ObjectStore: mem.Objects,
			key:         objectstore.BlobKey(small.ContentHash),
			size:        maxUnaryReadFileBytes + 1024,
		},
	}
	_, err = repoBounded.ReadFile(alice, &corev1.ReadFileRequest{
		Slice:    sliceRef,
		CommitId: landed.CommitId,
		Path:     "/acme/payment/seed.txt",
	})
	wantCode(t, err, codes.ResourceExhausted, "ReadFile streaming > 64 MiB with Length == 0")

	// 4. DiffChangeset caps per-file diff at 4 MiB (with 8 KiB binary sniff) and total diff output at 16 MiB.
	largeText := bytes.Repeat([]byte("a"), maxDiffFileBytes+128)
	largeBin := append([]byte{0x00, 0x01, 0x02}, bytes.Repeat([]byte("b"), maxDiffFileBytes+128)...)
	upText, err := h.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{Data: largeText, Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	upBin, err := h.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{Data: largeBin, Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	csDiff, err := h.Changeset.CreateChangeset(alice, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		BaseCommitId:   landed.CommitId,
		Title:          "large diff",
	})
	if err != nil {
		t.Fatal(err)
	}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/acme/payment/huge.txt", BlobId: upText.BlobId, ContentHash: upText.ContentHash, Mode: 0o100644},
		{Op: "upsert", Path: "/acme/payment/huge.bin", BlobId: upBin.BlobId, ContentHash: upBin.ContentHash, Mode: 0o100644},
	}
	// Add 6 medium text files (~3.5 MiB each) so total diff exceeds 16 MiB (maxDiffOutputBytes).
	chunkText := bytes.Repeat([]byte("line of diff output content here\n"), 110_000) // ~3.6 MiB < 4 MiB
	upChunk, err := h.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{Data: chunkText, Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		edits = append(edits, &corev1.FileEdit{
			Op:          "upsert",
			Path:        fmt.Sprintf("/acme/payment/part_%d.txt", i),
			BlobId:      upChunk.BlobId,
			ContentHash: upChunk.ContentHash,
			Mode:        0o100644,
		})
	}
	if _, err := h.Changeset.UpdateChangeset(alice, &corev1.UpdateChangesetRequest{
		ChangesetId:  csDiff.Id,
		BaseCommitId: landed.CommitId,
		FileEdits:    edits,
	}); err != nil {
		t.Fatal(err)
	}

	diffResp, err := h.Changeset.DiffChangeset(alice, &corev1.DiffChangesetRequest{ChangesetId: csDiff.Id})
	if err != nil {
		t.Fatalf("DiffChangeset: %v", err)
	}
	if !strings.Contains(diffResp.Diff, "Binary files /dev/null and b/acme/payment/huge.bin differ") {
		t.Fatalf("expected binary notice for oversized binary file in diff")
	}
	if !strings.Contains(diffResp.Diff, "Diff too large to render: a/acme/payment/huge.txt and b/acme/payment/huge.txt differ (exceeds 4 MiB per-file diff limit)") {
		t.Fatalf("expected large-file notice for oversized text file in diff")
	}
	if !strings.Contains(diffResp.Diff, "# diff output truncated: exceeds total diff size limit") {
		t.Fatalf("expected total diff truncation notice when total diff > 16 MiB")
	}
	if len(diffResp.Diff) > maxDiffOutputBytes+256 {
		t.Fatalf("diff output length %d exceeded cap %d", len(diffResp.Diff), maxDiffOutputBytes)
	}
}

type countingMissingGitBlobStore struct {
	storage.BlobStore
	calls atomic.Int64
}

func (c *countingMissingGitBlobStore) ListMissingGitBlobIDs(ctx context.Context, limit int) ([]*corev1.BlobRecord, error) {
	c.calls.Add(1)
	return c.BlobStore.ListMissingGitBlobIDs(ctx, limit)
}

type countingMirrorSliceStore struct {
	storage.SliceStore
	gets atomic.Int64
}

func (c *countingMirrorSliceStore) Get(ctx context.Context, sliceID string) (*corev1.Slice, error) {
	c.gets.Add(1)
	return c.SliceStore.Get(ctx, sliceID)
}

func TestGitBlobBackfillIdleWaitAndGitMirrorCoveringFanOut(t *testing.T) {
	t.Cleanup(func() {
		done := activity.Default.Begin()
		done()
	})

	mem := memory.New()
	mem.AddAccount("user_alice", "acme")

	// 1. RunGitBlobBackfill waits on activity.Default before its initial DB query.
	blobStore := &countingMissingGitBlobStore{BlobStore: mem.Blobs}
	blobSvc := &BlobService{
		Auth:        mem.Auth,
		Blobs:       blobStore,
		Slices:      mem.Slices,
		ObjectStore: mem.Objects,
	}

	activity.Default.ResetIdle()
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		blobSvc.RunGitBlobBackfill(ctx)
	}()

	time.Sleep(40 * time.Millisecond)
	if got := blobStore.calls.Load(); got != 0 {
		cancel()
		<-doneCh
		t.Fatalf("ListMissingGitBlobIDs called %d times while idle, want 0", got)
	}

	endReq := activity.Default.Begin()
	endReq()
	deadline := time.Now().Add(2 * time.Second)
	for blobStore.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-doneCh
	if blobStore.calls.Load() == 0 {
		t.Fatal("RunGitBlobBackfill did not run after activity woke the gate")
	}

	// 2. GitMirrors.FanOut uses CoveringIDsByPath with 0 Slices.Get calls across mirrored slices.
	touched := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "touched"}, []string{"/acme/touched"}, "private")
	if _, err := mem.GitMirrors.PutGitMirror(context.Background(), storage.GitMirror{
		SliceID:   touched.Id,
		URL:       "https://github.com/acme/touched.git",
		CreatedBy: "user_alice",
	}); err != nil {
		t.Fatal(err)
	}
	untouchedIDs := make([]string, 0, 20)
	for i := range 20 {
		other := mem.PutSlice(
			&corev1.SliceRef{Account: "acme", Slice: fmt.Sprintf("other_%d", i)},
			[]string{fmt.Sprintf("/acme/other_%d", i)},
			"private",
		)
		untouchedIDs = append(untouchedIDs, other.Id)
		if _, err := mem.GitMirrors.PutGitMirror(context.Background(), storage.GitMirror{
			SliceID:   other.Id,
			URL:       fmt.Sprintf("https://github.com/acme/other_%d.git", i),
			CreatedBy: "user_alice",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Clear initial pending state from PutGitMirror? Let's check: PutGitMirror sets PendingSeq=1,
	// so finish them first so Pending() is false before FanOut!
	for _, id := range append([]string{touched.Id}, untouchedIDs...) {
		m, err := mem.GitMirrors.GetGitMirror(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err := mem.GitMirrors.FinishGitMirror(context.Background(), id, storage.GitMirrorPush{
			Seq:    m.PendingSeq,
			OK:     true,
			Commit: "init",
			At:     time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	sliceStore := &countingMirrorSliceStore{SliceStore: mem.Slices}
	mirrors := &GitMirrors{
		Mirrors:             mem.GitMirrors,
		Slices:              sliceStore,
		AllowPrivateTargets: true,
	}
	mirrors.FanOut(context.Background(), storage.Event{
		Kind:         storage.EventChangesetSubmitted,
		TargetRef:    storage.DefaultTargetRef,
		ChangedPaths: []string{"/acme/touched/file.go"},
	})

	if gets := sliceStore.gets.Load(); gets != 0 {
		t.Fatalf("GitMirrors.FanOut Slices.Get calls = %d, want 0", gets)
	}
	tm, err := mem.GitMirrors.GetGitMirror(context.Background(), touched.Id)
	if err != nil || !tm.Pending() {
		t.Fatalf("touched mirror pending = %v (err=%v), want true", tm != nil && tm.Pending(), err)
	}
	um, err := mem.GitMirrors.GetGitMirror(context.Background(), untouchedIDs[0])
	if err != nil || um.Pending() {
		t.Fatalf("untouched mirror pending = %v (err=%v), want false", um != nil && um.Pending(), err)
	}
}
