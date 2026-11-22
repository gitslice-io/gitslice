package cli

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestApplyWorkspaceSyncMergeStrategies(t *testing.T) {
	const path = "/acme/payment/strategy.go"
	t.Run("manual keeps conflicts", func(t *testing.T) {
		plan := workspaceSyncPlan{
			LocalPaths: []string{path},
			Conflicts:  []WorkspaceConflict{{Path: path}},
		}
		if err := (Runner{}).applyWorkspaceSyncMergeStrategy(context.Background(), nil, nil, "", "", workspaceSyncMergeManual, BaseSnapshot{}, nil, nil, &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Conflicts) != 1 || len(plan.LocalPaths) != 1 {
			t.Fatalf("manual strategy changed plan unexpectedly: %#v", plan)
		}
	})
	t.Run("ours keeps local side", func(t *testing.T) {
		plan := workspaceSyncPlan{
			LocalPaths: []string{path},
			Conflicts:  []WorkspaceConflict{{Path: path}},
		}
		if err := (Runner{}).applyWorkspaceSyncMergeStrategy(context.Background(), nil, nil, "", "", workspaceSyncMergeOurs, BaseSnapshot{}, nil, nil, &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Conflicts) != 0 || !stringSliceContains(plan.LocalPaths, path) || len(plan.RemoteUpserts) != 0 {
			t.Fatalf("ours strategy plan = %#v", plan)
		}
	})
	t.Run("theirs takes remote side", func(t *testing.T) {
		plan := workspaceSyncPlan{
			LocalPaths: []string{path},
			Conflicts:  []WorkspaceConflict{{Path: path}},
		}
		remote := map[string]remoteWorkspaceFile{
			path: {BaseSnapshotFile: BaseSnapshotFile{Path: path}},
		}
		if err := (Runner{}).applyWorkspaceSyncMergeStrategy(context.Background(), nil, nil, "", "", workspaceSyncMergeTheirs, BaseSnapshot{}, nil, remote, &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Conflicts) != 0 || len(plan.LocalPaths) != 0 || len(plan.RemoteUpserts) != 1 || !stringSliceContains(plan.UpdatedPaths, path) {
			t.Fatalf("theirs strategy plan = %#v", plan)
		}
	})
}

func TestWorkspaceMetadataStoreCommitBaseSynchronizesAllFiles(t *testing.T) {
	wsDir := t.TempDir()
	r := Runner{Home: t.TempDir(), Dir: wsDir}
	if err := writeJSONFile(filepath.Join(wsDir, ".gs", "slice.json"), WorkspaceConfig{
		Account:       "acme",
		Slice:         "app",
		IncludedPaths: []string{"/acme/app"},
		BaseCommitID:  "sha256:old",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.writeWorkspaceState(WorkspaceState{
		CurrentChangesetID: "cs_1",
		CurrentPatchsetID:  "ps_1",
		BaseCommitID:       "sha256:old",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.writeBaseSnapshot(BaseSnapshot{CommitID: "sha256:old"}); err != nil {
		t.Fatal(err)
	}

	store, err := r.openWorkspaceMetadataStore()
	if err != nil {
		t.Fatalf("openWorkspaceMetadataStore failed: %v", err)
	}
	conflict := WorkspaceConflictState{
		OldBaseCommitID: "sha256:old",
		NewBaseCommitID: "sha256:new",
		ChangesetID:     "cs_1",
		PatchsetID:      "ps_2",
		Conflicts:       []WorkspaceConflict{{Path: "/acme/app/a.txt", ConflictClass: "content"}},
	}
	nextState := store.State
	nextState.CurrentPatchsetID = "ps_2"
	if err := store.CommitBase(store.Config, nextState, BaseSnapshot{CommitID: "sha256:new"}, &conflict); err != nil {
		t.Fatalf("CommitBase failed: %v", err)
	}

	reopened, err := r.openWorkspaceMetadataStore()
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Config.BaseCommitID != "sha256:new" || reopened.State.BaseCommitID != "sha256:new" || reopened.Snapshot.CommitID != "sha256:new" {
		t.Fatalf("expected all 3 .gs files at sha256:new, got slice=%q state=%q snapshot=%q",
			reopened.Config.BaseCommitID, reopened.State.BaseCommitID, reopened.Snapshot.CommitID)
	}
	gotConflict, hasConflict, err := r.readWorkspaceConflictState()
	if err != nil || !hasConflict || len(gotConflict.Conflicts) != 1 {
		t.Fatalf("expected conflict state persisted, got has=%v conflicts=%+v err=%v", hasConflict, gotConflict, err)
	}

	// Clearing conflicts via empty WorkspaceConflictState removes conflicts.json.
	if err := reopened.CommitBase(reopened.Config, reopened.State, BaseSnapshot{CommitID: "sha256:newer"}, &WorkspaceConflictState{}); err != nil {
		t.Fatal(err)
	}
	if _, hasConflict, err := r.readWorkspaceConflictState(); err != nil || hasConflict {
		t.Fatalf("expected conflicts.json cleared, got has=%v err=%v", hasConflict, err)
	}
}

type failingSyncRPCServer struct {
	corev1.UnimplementedRepositoryServiceServer
	corev1.UnimplementedChangesetServiceServer
	corev1.UnimplementedBlobServiceServer
}

func (s *failingSyncRPCServer) GetRef(context.Context, *corev1.GetRefRequest) (*corev1.Ref, error) {
	return &corev1.Ref{Name: "refs/global/main", CommitId: "sha256:new_head"}, nil
}

func (s *failingSyncRPCServer) GetChangeset(_ context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	return &corev1.Changeset{Id: req.GetChangesetId(), Status: "draft", CurrentPatchsetId: "ps_1"}, nil
}

func (s *failingSyncRPCServer) ListDirectory(context.Context, *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	return &corev1.ListDirectoryResponse{}, nil
}

func (s *failingSyncRPCServer) UpdateChangeset(context.Context, *corev1.UpdateChangesetRequest) (*corev1.Patchset, error) {
	return nil, grpcstatus.Error(codes.Internal, "simulated mid-sync RPC failure")
}

func TestWorkspaceSyncDoesNotCorruptGsMetadataWhenUpdateChangesetFails(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	stub := &failingSyncRPCServer{}
	corev1.RegisterRepositoryServiceServer(srv, stub)
	corev1.RegisterChangesetServiceServer(srv, stub)
	corev1.RegisterBlobServiceServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	home := t.TempDir()
	wsDir := t.TempDir()
	r := Runner{Home: home, Dir: wsDir}
	if err := r.writeUserConfig(UserConfig{ServerAddr: lis.Addr().String(), Token: "tok_test", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(wsDir, ".gs", "slice.json"), WorkspaceConfig{
		Account:       "acme",
		Slice:         "app",
		IncludedPaths: []string{"/acme/app"},
		BaseCommitID:  "sha256:old_head",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.writeWorkspaceState(WorkspaceState{
		CurrentChangesetID: "cs_open_1",
		CurrentPatchsetID:  "ps_1",
		BaseCommitID:       "sha256:old_head",
		ActiveStackID:      "",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.writeBaseSnapshot(BaseSnapshot{CommitID: "sha256:old_head"}); err != nil {
		t.Fatal(err)
	}

	if err := r.runWorkspaceSync(context.Background(), commandOptions{Format: "text"}, defaultWorkspaceSyncMergeStrategy); err == nil {
		t.Fatal("expected runWorkspaceSync to fail when UpdateChangeset fails")
	}

	after, err := r.openWorkspaceMetadataStore()
	if err != nil {
		t.Fatal(err)
	}
	if after.Config.BaseCommitID != "sha256:old_head" || after.State.BaseCommitID != "sha256:old_head" || after.Snapshot.CommitID != "sha256:old_head" {
		t.Fatalf("mid-sync RPC failure left .gs metadata split across commits: slice=%q state=%q snapshot=%q",
			after.Config.BaseCommitID, after.State.BaseCommitID, after.Snapshot.CommitID)
	}
}

func TestStackSnapshotThroughChangesetFromBaseDoesNotMutateBaseSnapshotFiles(t *testing.T) {
	r := Runner{}
	ws := WorkspaceConfig{
		Account:       "acme",
		Slice:         "app",
		IncludedPaths: []string{"/acme/app"},
	}
	base := BaseSnapshot{
		CommitID: "sha256:base",
		Files: map[string]BaseSnapshotFile{
			"orig.txt": {ContentHash: "hash_orig", Mode: 0o100644, Size: 10},
		},
	}
	stack := &corev1.ChangesetStack{
		BaseCommitId: "sha256:base",
		Entries: []*corev1.ChangesetStackEntry{
			{
				ChangesetId: "cs_1",
				Changeset: &corev1.Changeset{
					Id:                "cs_1",
					CurrentPatchsetId: "ps_1",
					Patchsets: []*corev1.Patchset{
						{
							Id: "ps_1",
							FileEdits: []*corev1.FileEdit{
								{
									Path:        "/acme/app/new.txt",
									ContentHash: "hash_new",
									Mode:        0o100644,
								},
							},
						},
					},
				},
			},
		},
	}

	result, err := r.stackSnapshotThroughChangesetFromBase(context.Background(), nil, UserConfig{}, ws, base, stack, "cs_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := result.Files["/acme/app/new.txt"]; !ok {
		t.Fatalf("expected /acme/app/new.txt in result.Files, got: %v", result.Files)
	}
	if _, ok := base.Files["/acme/app/new.txt"]; ok {
		t.Fatalf("base.Files was mutated in-place by stackSnapshotThroughChangesetFromBase: %v", base.Files)
	}
	if len(base.Files) != 1 {
		t.Fatalf("expected base.Files len 1, got %d", len(base.Files))
	}
}
