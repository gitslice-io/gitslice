package cli

import (
	"context"
	"net"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeStackMoveServer struct {
	stackBefore *corev1.ChangesetStack
	stackAfter  *corev1.ChangesetStack
	changesets  map[string]*corev1.Changeset
	reparentReq *corev1.ReparentStackEntryRequest
	restackReq  *corev1.RestackRequest
}

func newFakeStackMoveServer() *fakeStackMoveServer {
	root := fakeStackChangeset("cs_root", "ps_root_1")
	parent := fakeStackChangeset("cs_parent", "ps_parent_1")
	movedBefore := fakeStackChangeset("cs_moved", "ps_moved_1")
	movedAfter := fakeStackChangeset("cs_moved", "ps_moved_2")
	stackBefore := &corev1.ChangesetStack{
		Id:            "stk_1",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_moved",
		RootEntryId:   "cs_root",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_1", ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
			{StackId: "stk_1", ChangesetId: "cs_parent", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 2, SiblingOrder: 1, Depth: 1, State: "draft", Changeset: parent},
			{StackId: "stk_1", ChangesetId: "cs_moved", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 3, SiblingOrder: 2, Depth: 1, State: "draft", Changeset: movedBefore},
		},
	}
	stackAfter := &corev1.ChangesetStack{
		Id:            "stk_1",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_moved",
		RootEntryId:   "cs_root",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_1", ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
			{StackId: "stk_1", ChangesetId: "cs_parent", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 2, SiblingOrder: 1, Depth: 1, State: "draft", Changeset: parent},
			{StackId: "stk_1", ChangesetId: "cs_moved", ParentChangesetId: "cs_parent", ParentPatchsetId: "ps_parent_1", DisplayOrder: 3, SiblingOrder: 1, Depth: 2, State: "draft", Changeset: movedAfter},
		},
	}
	return &fakeStackMoveServer{
		stackBefore: stackBefore,
		stackAfter:  stackAfter,
		changesets: map[string]*corev1.Changeset{
			"cs_root":   root,
			"cs_parent": parent,
			"cs_moved":  movedAfter,
		},
	}
}

func fakeStackChangeset(id, patchsetID string) *corev1.Changeset {
	return &corev1.Changeset{
		Id:                    id,
		StackId:               "stk_1",
		BaseCommitId:          "cmt_base",
		TargetRef:             "refs/global/main",
		Status:                "draft",
		CurrentPatchsetId:     patchsetID,
		CurrentPatchsetNumber: 1,
		Patchsets: []*corev1.Patchset{{
			Id:           patchsetID,
			ChangesetId:  id,
			Number:       1,
			BaseCommitId: "cmt_base",
			BaseKind:     "commit",
			FileEdits:    []*corev1.FileEdit{},
		}},
	}
}

type fakeStackMoveStackService struct {
	corev1.UnimplementedChangesetStackServiceServer
	state *fakeStackMoveServer
}

func (f fakeStackMoveStackService) GetStack(ctx context.Context, req *corev1.GetStackRequest) (*corev1.ChangesetStack, error) {
	if f.state.restackReq != nil {
		return f.state.stackAfter, nil
	}
	return f.state.stackBefore, nil
}

func (f fakeStackMoveStackService) ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error) {
	f.state.reparentReq = req
	return f.state.stackAfter, nil
}

func (f fakeStackMoveStackService) Restack(ctx context.Context, req *corev1.RestackRequest) (*corev1.RestackResponse, error) {
	f.state.restackReq = req
	return &corev1.RestackResponse{StackId: req.StackId, Status: "clean", Entries: []*corev1.Changeset{f.state.changesets["cs_moved"]}}, nil
}

type fakeStackMoveChangesetService struct {
	corev1.UnimplementedChangesetServiceServer
	state *fakeStackMoveServer
}

func (f fakeStackMoveChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	cs := f.state.changesets[req.ChangesetId]
	if cs == nil {
		return nil, status.Error(codes.NotFound, "changeset not found")
	}
	return cs, nil
}

func startFakeStackMoveServer(t *testing.T, state *fakeStackMoveServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackMoveStackService{state: state})
	corev1.RegisterChangesetServiceServer(server, fakeStackMoveChangesetService{state: state})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack move server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}

type fakeStackSubmitServer struct {
	submitReq         *corev1.SubmitStackRequest
	getChangesetCalls int
	getRefCalls       int
}

func newFakeStackSubmitServer() *fakeStackSubmitServer {
	return &fakeStackSubmitServer{}
}

type fakeStackSubmitStackService struct {
	corev1.UnimplementedChangesetStackServiceServer
	state *fakeStackSubmitServer
}

func (f fakeStackSubmitStackService) SubmitStack(ctx context.Context, req *corev1.SubmitStackRequest) (*corev1.SubmitStackResponse, error) {
	f.state.submitReq = req
	return &corev1.SubmitStackResponse{
		StackId: req.StackId,
		Status:  "submitted",
		Results: []*corev1.SubmitStackEntryResult{{
			ChangesetId:      "cs_root",
			Status:           "pending_publish",
			PendingPublishId: "pending_root",
		}},
	}, nil
}

type fakeStackSubmitChangesetService struct {
	corev1.UnimplementedChangesetServiceServer
	state *fakeStackSubmitServer
}

func (f fakeStackSubmitChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	f.state.getChangesetCalls++
	return &corev1.Changeset{
		Id:                req.ChangesetId,
		StackId:           "stk_1",
		Status:            "submitted",
		CommitId:          "commit_root",
		CurrentPatchsetId: "ps_root_1",
	}, nil
}

type fakeStackSubmitRepositoryService struct {
	corev1.UnimplementedRepositoryServiceServer
	state *fakeStackSubmitServer
}

func (f fakeStackSubmitRepositoryService) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	f.state.getRefCalls++
	return &corev1.Ref{Name: req.RefName, CommitId: "commit_root"}, nil
}

func startFakeStackSubmitServer(t *testing.T, state *fakeStackSubmitServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackSubmitStackService{state: state})
	corev1.RegisterChangesetServiceServer(server, fakeStackSubmitChangesetService{state: state})
	corev1.RegisterRepositoryServiceServer(server, fakeStackSubmitRepositoryService{state: state})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack submit server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}

type fakeStackDetachServer struct {
	sourceBefore   *corev1.ChangesetStack
	sourceAfter    *corev1.ChangesetStack
	detachedBefore *corev1.ChangesetStack
	detachedAfter  *corev1.ChangesetStack
	changesets     map[string]*corev1.Changeset
	detachReq      *corev1.DetachStackEntryRequest
	restackReq     *corev1.RestackRequest
}

func newFakeStackDetachServer() *fakeStackDetachServer {
	root := fakeStackChangesetForStack("stk_source", "cs_root", "ps_root_1")
	childBefore := fakeStackChangesetForStack("stk_source", "cs_child", "ps_child_1")
	childAfter := fakeStackChangesetForStack("stk_detached", "cs_child", "ps_child_2")
	sourceBefore := &corev1.ChangesetStack{
		Id:            "stk_source",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_child",
		RootEntryId:   "cs_root",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_source", ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
			{StackId: "stk_source", ChangesetId: "cs_child", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 2, SiblingOrder: 1, Depth: 1, State: "draft", Changeset: childBefore},
		},
	}
	sourceAfter := &corev1.ChangesetStack{
		Id:            "stk_source",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_root",
		RootEntryId:   "cs_root",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_source", ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
		},
	}
	detachedBefore := &corev1.ChangesetStack{
		Id:            "stk_detached",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_child",
		RootEntryId:   "cs_child",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_detached", ChangesetId: "cs_child", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "needs_restack", Changeset: childBefore},
		},
	}
	detachedAfter := &corev1.ChangesetStack{
		Id:            "stk_detached",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_child",
		RootEntryId:   "cs_child",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_detached", ChangesetId: "cs_child", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: childAfter},
		},
	}
	return &fakeStackDetachServer{
		sourceBefore:   sourceBefore,
		sourceAfter:    sourceAfter,
		detachedBefore: detachedBefore,
		detachedAfter:  detachedAfter,
		changesets: map[string]*corev1.Changeset{
			"cs_root":  root,
			"cs_child": childAfter,
		},
	}
}

func fakeStackChangesetForStack(stackID, id, patchsetID string) *corev1.Changeset {
	cs := fakeStackChangeset(id, patchsetID)
	cs.StackId = stackID
	return cs
}

type fakeStackDetachStackService struct {
	corev1.UnimplementedChangesetStackServiceServer
	state *fakeStackDetachServer
}

func (f fakeStackDetachStackService) GetStack(ctx context.Context, req *corev1.GetStackRequest) (*corev1.ChangesetStack, error) {
	switch req.StackId {
	case "stk_source":
		if f.state.detachReq != nil {
			return f.state.sourceAfter, nil
		}
		return f.state.sourceBefore, nil
	case "stk_detached":
		if f.state.restackReq != nil {
			return f.state.detachedAfter, nil
		}
		return f.state.detachedBefore, nil
	default:
		return nil, status.Error(codes.NotFound, "stack not found")
	}
}

func (f fakeStackDetachStackService) DetachStackEntry(ctx context.Context, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error) {
	f.state.detachReq = req
	return &corev1.DetachStackEntryResponse{SourceStack: f.state.sourceAfter, DetachedStack: f.state.detachedBefore}, nil
}

func (f fakeStackDetachStackService) Restack(ctx context.Context, req *corev1.RestackRequest) (*corev1.RestackResponse, error) {
	f.state.restackReq = req
	return &corev1.RestackResponse{StackId: req.StackId, Status: "clean", Entries: []*corev1.Changeset{f.state.changesets["cs_child"]}}, nil
}

type fakeStackDetachChangesetService struct {
	corev1.UnimplementedChangesetServiceServer
	state *fakeStackDetachServer
}

func (f fakeStackDetachChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	cs := f.state.changesets[req.ChangesetId]
	if cs == nil {
		return nil, status.Error(codes.NotFound, "changeset not found")
	}
	return cs, nil
}

func startFakeStackDetachServer(t *testing.T, state *fakeStackDetachServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackDetachStackService{state: state})
	corev1.RegisterChangesetServiceServer(server, fakeStackDetachChangesetService{state: state})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack detach server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}

type fakeStackRestackConflictService struct {
	corev1.UnimplementedChangesetStackServiceServer
}

func (fakeStackRestackConflictService) Restack(ctx context.Context, req *corev1.RestackRequest) (*corev1.RestackResponse, error) {
	return &corev1.RestackResponse{
		StackId: req.StackId,
		Status:  "conflicts",
		Entries: []*corev1.Changeset{{
			Id:                "cs_child",
			StackId:           req.StackId,
			BaseCommitId:      "cmt_base",
			CurrentPatchsetId: "ps_child_2",
			Patchsets: []*corev1.Patchset{{
				Id:           "ps_child_2",
				ChangesetId:  "cs_child",
				Number:       2,
				BaseCommitId: "cmt_base",
				Conflicts: []*corev1.PatchsetConflict{{
					Path:            "/acme/payment/conflict.go",
					ConflictClass:   "restack",
					OldBaseCommitId: "cmt_base",
					NewBaseCommitId: "cmt_base",
				}},
			}},
		}},
	}, nil
}

func (fakeStackRestackConflictService) GetStack(ctx context.Context, req *corev1.GetStackRequest) (*corev1.ChangesetStack, error) {
	root := &corev1.Changeset{
		Id:                "cs_root",
		StackId:           req.StackId,
		BaseCommitId:      "cmt_base",
		CurrentPatchsetId: "ps_root_1",
		Patchsets: []*corev1.Patchset{{
			Id:           "ps_root_1",
			ChangesetId:  "cs_root",
			Number:       1,
			BaseCommitId: "cmt_base",
		}},
	}
	child := &corev1.Changeset{
		Id:                "cs_child",
		StackId:           req.StackId,
		ParentChangesetId: "cs_root",
		ParentPatchsetId:  "ps_root_1",
		BaseCommitId:      "cmt_base",
		CurrentPatchsetId: "ps_child_2",
		Patchsets: []*corev1.Patchset{{
			Id:           "ps_child_2",
			ChangesetId:  "cs_child",
			Number:       2,
			BaseCommitId: "cmt_base",
			Conflicts: []*corev1.PatchsetConflict{{
				Path:            "/acme/payment/conflict.go",
				ConflictClass:   "restack",
				OldBaseCommitId: "cmt_base",
				NewBaseCommitId: "cmt_base",
			}},
		}},
	}
	return &corev1.ChangesetStack{
		Id:            req.StackId,
		BaseCommitId:  "cmt_base",
		RootEntryId:   "cs_root",
		ActiveEntryId: "cs_child",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: req.StackId, ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
			{StackId: req.StackId, ChangesetId: "cs_child", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 2, SiblingOrder: 1, Depth: 1, State: "draft", Changeset: child},
		},
	}, nil
}

func startFakeStackRestackConflictServer(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackRestackConflictService{})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack restack conflict server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}

type fakeStackSyncRepositoryService struct {
	corev1.UnimplementedRepositoryServiceServer
	commitID string
}

func (f fakeStackSyncRepositoryService) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	return &corev1.Ref{Name: req.RefName, CommitId: f.commitID}, nil
}

func (f fakeStackSyncRepositoryService) ResolvePath(ctx context.Context, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	return nil, status.Error(codes.NotFound, "path not found")
}

func startFakeStackSyncServer(t *testing.T, state *fakeStackMoveServer, commitID string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackMoveStackService{state: state})
	corev1.RegisterRepositoryServiceServer(server, fakeStackSyncRepositoryService{commitID: commitID})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack sync server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}
