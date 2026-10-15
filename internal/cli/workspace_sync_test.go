package cli

import (
	"context"
	"testing"
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
