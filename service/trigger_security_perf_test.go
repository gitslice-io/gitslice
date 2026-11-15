package service

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTriggerTestServices(mem *memory.Stores, dispatchSecret string) (*triggers.Engine, *TriggerService, *TriggerRunService) {
	tree := TreeFiles{Repository: mem.Repository, Objects: mem.Objects}
	engine := triggers.NewEngine(triggers.Stores{
		Triggers:   mem.Triggers,
		Changesets: mem.Changesets,
		Slices:     mem.Slices,
		Repository: mem.Repository,
		Checks:     mem.Checks,
	}, triggers.Options{
		Tree:                tree,
		DispatchURL:         "https://triggers.test",
		DispatchSecret:      dispatchSecret,
		AllowPrivateTargets: true,
	})
	trigSvc := &TriggerService{
		Auth:       mem.Auth,
		Changesets: mem.Changesets,
		Slices:     mem.Slices,
		Repository: mem.Repository,
		Triggers:   mem.Triggers,
		Tree:       tree,
		Engine:     engine,
	}
	runSvc := &TriggerRunService{
		Changesets: mem.Changesets,
		Slices:     mem.Slices,
		Repository: mem.Repository,
		Checks:     mem.Checks,
		Triggers:   mem.Triggers,
		Tree:       tree,
		Engine:     engine,
	}
	return engine, trigSvc, runSvc
}

func TestTriggerServiceAuthorizationAndRefGuards(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	_, trigSvc, _ := newTriggerTestServices(mem, "test-dispatch-secret")

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

	// 1. ListTriggers requires authentication and restricts ref to default target ref.
	_, err := trigSvc.ListTriggers(anon, &corev1.ListTriggersRequest{Path: "/acme/public"})
	wantCode(t, err, codes.Unauthenticated, "ListTriggers unauthenticated")

	_, err = trigSvc.ListTriggers(alice, &corev1.ListTriggersRequest{Path: "/acme/public", Ref: "refs/heads/secret-branch"})
	wantCode(t, err, codes.InvalidArgument, "ListTriggers non-default ref")

	if _, err := trigSvc.ListTriggers(alice, &corev1.ListTriggersRequest{Path: "/acme/public", Ref: "refs/global/main"}); err != nil {
		t.Fatalf("ListTriggers default ref for admin: %v", err)
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

	specBytes, _ := json.Marshal(triggers.RunSpec{
		Name: "audit",
		Trigger: triggers.Trigger{
			On:      []string{"changeset.opened"},
			Timeout: time.Minute,
			Action:  triggers.Action{Kind: triggers.ActionWorker, Worker: "audit.js"},
		},
	})
	inserted, err := mem.Triggers.CreateTriggerRuns(context.Background(), []storage.TriggerRun{{
		ID:                 "trun_secret_folder",
		Account:            "acme",
		TriggerID:          "secret-ops:audit",
		Folder:             "/acme/secret-ops",
		Action:             triggers.ActionWorker,
		EventID:            "ev_1",
		EventKind:          "changeset.opened",
		ChangesetID:        cs.Id,
		DefinitionCommitID: head.CommitId,
		Spec:               specBytes,
		Payload:            []byte(`{}`),
		Status:             storage.TriggerRunRunning,
		MaxAttempts:        1,
		CreatedAt:          time.Now().UTC(),
	}})
	if err != nil || len(inserted) != 1 {
		t.Fatalf("CreateTriggerRuns: %v (len=%d)", err, len(inserted))
	}
	if _, err := mem.Triggers.AppendTriggerRunLog(context.Background(), "trun_secret_folder", "stdout", "internal log line"); err != nil {
		t.Fatal(err)
	}

	// 2. ListTriggerRunLogs requires auth AND read access covering run.Folder (/acme/secret-ops).
	_, err = trigSvc.ListTriggerRunLogs(anon, &corev1.ListTriggerRunLogsRequest{RunId: "trun_secret_folder"})
	wantCode(t, err, codes.Unauthenticated, "ListTriggerRunLogs unauthenticated on public changeset")

	_, err = trigSvc.ListTriggerRunLogs(outsider, &corev1.ListTriggerRunLogsRequest{RunId: "trun_secret_folder"})
	if status.Code(err) != codes.NotFound && status.Code(err) != codes.PermissionDenied {
		t.Fatalf("outsider ListTriggerRunLogs error = %v, want NotFound or PermissionDenied", err)
	}

	// opsWriter covers /acme/secret-ops and pubSlice is public (ActionRead passes on changeset).
	logsResp, err := trigSvc.ListTriggerRunLogs(opsWriter, &corev1.ListTriggerRunLogsRequest{RunId: "trun_secret_folder"})
	if err != nil || len(logsResp.GetLogs()) != 1 {
		t.Fatalf("opsWriter ListTriggerRunLogs = %+v, %v", logsResp, err)
	}

	// 3. CancelTriggerRun & RerunTriggerRun require auth + ActionWrite on both changeset and run.Folder.
	_, err = trigSvc.CancelTriggerRun(anon, &corev1.CancelTriggerRunRequest{RunId: "trun_secret_folder"})
	wantCode(t, err, codes.Unauthenticated, "CancelTriggerRun unauthenticated")

	_, err = trigSvc.CancelTriggerRun(reader, &corev1.CancelTriggerRunRequest{RunId: "trun_secret_folder"})
	wantCode(t, err, codes.PermissionDenied, "CancelTriggerRun reader")

	// bob can write pubSlice (the changeset's authoring slice) but NOT /acme/secret-ops!
	_, err = trigSvc.CancelTriggerRun(bob, &corev1.CancelTriggerRunRequest{RunId: "trun_secret_folder"})
	if status.Code(err) != codes.NotFound && status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bob CancelTriggerRun without folder write access = %v, want NotFound or PermissionDenied", err)
	}
	_, err = trigSvc.RerunTriggerRun(bob, &corev1.RerunTriggerRunRequest{RunId: "trun_secret_folder"})
	if status.Code(err) != codes.NotFound && status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bob RerunTriggerRun without folder write access = %v, want NotFound or PermissionDenied", err)
	}

	// Account owner alice can cancel and rerun.
	if _, err := trigSvc.CancelTriggerRun(alice, &corev1.CancelTriggerRunRequest{RunId: "trun_secret_folder"}); err != nil {
		t.Fatalf("alice CancelTriggerRun: %v", err)
	}
	if _, err := trigSvc.RerunTriggerRun(alice, &corev1.RerunTriggerRunRequest{RunId: "trun_secret_folder"}); err != nil {
		t.Fatalf("alice RerunTriggerRun: %v", err)
	}
}

type mapTreeReader map[string]string

func (m mapTreeReader) ReadFile(_ context.Context, _, p string) ([]byte, error) {
	v, ok := m[p]
	if !ok {
		return nil, triggers.ErrNotFound
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

	// 2. CodeHash binds Account and Folder, and StartCallback atomically claims TriggerRunWaiting -> TriggerRunRunning.
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccount("user_other", "otherorg")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "ops"}, []string{"/acme/ops"}, "private")
	mem.PutSlice(&corev1.SliceRef{Account: "otherorg", Slice: "payment"}, []string{"/otherorg/payment"}, "private")

	const secret = "dispatch-secret-xyz"
	engine, _, runSvc := newTriggerTestServices(mem, secret)
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

	specBytes, _ := json.Marshal(triggers.RunSpec{
		Name: "w",
		Trigger: triggers.Trigger{
			On:      []string{"push"},
			Timeout: 2 * time.Minute,
			Action:  triggers.Action{Kind: triggers.ActionWorker, Worker: "worker.js"},
		},
	})
	now := time.Now().UTC()
	_, err = mem.Triggers.CreateTriggerRuns(context.Background(), []storage.TriggerRun{
		{ID: "run_acme_pay", Account: "acme", TriggerID: "payment:w", Folder: "/acme/payment", Action: triggers.ActionWorker, EventID: "e1", EventKind: "push", DefinitionCommitID: c1, Spec: specBytes, Payload: []byte(`{}`), Status: storage.TriggerRunQueued, MaxAttempts: 1, CreatedAt: now},
		{ID: "run_acme_ops", Account: "acme", TriggerID: "ops:w", Folder: "/acme/ops", Action: triggers.ActionWorker, EventID: "e2", EventKind: "push", DefinitionCommitID: c2, Spec: specBytes, Payload: []byte(`{}`), Status: storage.TriggerRunQueued, MaxAttempts: 1, CreatedAt: now},
		{ID: "run_other_pay", Account: "otherorg", TriggerID: "payment:w", Folder: "/otherorg/payment", Action: triggers.ActionWorker, EventID: "e3", EventKind: "push", DefinitionCommitID: c3, Spec: specBytes, Payload: []byte(`{}`), Status: storage.TriggerRunQueued, MaxAttempts: 1, CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	r1, err := runSvc.StartRun(context.Background(), &corev1.StartTriggerRunRequest{RunId: "run_acme_pay", DispatcherKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := runSvc.StartRun(context.Background(), &corev1.StartTriggerRunRequest{RunId: "run_acme_ops", DispatcherKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	r3, err := runSvc.StartRun(context.Background(), &corev1.StartTriggerRunRequest{RunId: "run_other_pay", DispatcherKey: secret})
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
	if _, err := runSvc.FinishRun(context.Background(), &corev1.FinishTriggerRunRequest{
		RunToken: r1.RunToken,
		Outcome:  "succeeded",
		Wait:     true,
	}); err != nil {
		t.Fatal(err)
	}

	sig := engine.CallbackSignature("run_acme_pay")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	resps := make([]*corev1.StartTriggerRunResponse, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resps[idx], errs[idx] = runSvc.StartCallback(context.Background(), &corev1.StartTriggerRunRequest{
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
	finished, err := runSvc.FinishRun(context.Background(), &corev1.FinishTriggerRunRequest{
		RunToken: cbToken,
		Outcome:  "succeeded",
		Finish:   true,
	})
	if err != nil || finished.GetStatus() != storage.TriggerRunSucceeded {
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
