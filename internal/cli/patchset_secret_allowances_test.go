package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/service"
)

func TestCheckAllowCLIAndSchema(t *testing.T) {
	ctx := context.Background()
	const (
		aliceToken = "alice-allow-tok"
		bobToken   = "bob-allow-tok"
		danaToken  = "dana-allow-tok"
	)
	serverAddr, mem := startAgentSignupServerWithStores(t, false, map[string]string{
		aliceToken: "user_alice",
		bobToken:   "user_bob",
		danaToken:  "user_dana",
	})

	mem.AddPersonalAccount("user_alice", "alice")
	mem.AddPersonalAccount("user_bob", "bob")
	mem.AddPersonalAccount("user_dana", "dana")
	mem.AddAccountRole("user_alice", "acme", "owner")

	slice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")
	if _, err := mem.Slices.SetMember(ctx, slice.GetId(), "user_bob", "contributor", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetMember(ctx, slice.GetId(), "user_dana", "writer", "user_alice"); err != nil {
		t.Fatal(err)
	}

	checksYAML := []byte("version: 1\nchecks:\n  unit:\n    run: echo ok\n")
	checksHash := objectid.RawContentHash(checksYAML)
	mem.PutObject(filesystem.BlobKey(checksHash), checksYAML)
	mem.PutCommitWithFiles("commit_cli_allow_seed", []storage.FileEntry{{
		Path:        "/acme/payment/.gitslice/checks.yaml",
		BlobID:      objectid.BlobID(checksYAML),
		ContentHash: checksHash,
		Mode:        0o100644,
		Size:        int64(len(checksYAML)),
	}}, []string{"/acme/payment/.gitslice/checks.yaml"})

	daemon, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: "user_alice",
		Account:   "acme",
		Name:      "cli-allow-ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Agents.ExposeDaemon(ctx, daemon.GetId(), slice.GetId(), "user_alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetCIDaemons(ctx, slice.GetId(), []string{daemon.GetId()}); err != nil {
		t.Fatal(err)
	}

	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)
	ref, err := mem.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	bobCtx := authctx.WithSubjectID(ctx, "user_bob")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}

	createPatch := func(title string) (*corev1.Changeset, *corev1.Patchset, *corev1.Patchset) {
		t.Helper()
		cs, err := handlers.Changeset.CreateChangeset(bobCtx, &corev1.CreateChangesetRequest{
			AuthoringSlice: sliceRef,
			TargetRef:      storage.DefaultTargetRef,
			BaseCommitId:   ref.CommitId,
			Title:          title,
		})
		if err != nil {
			t.Fatal(err)
		}
		up := func(path, content string) *corev1.Patchset {
			upload, err := handlers.Blob.UploadBlob(bobCtx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: sliceRef})
			if err != nil {
				t.Fatal(err)
			}
			ps, err := handlers.Changeset.UpdateChangeset(bobCtx, &corev1.UpdateChangesetRequest{
				ChangesetId:  cs.GetId(),
				BaseCommitId: ref.CommitId,
				FileEdits: []*corev1.FileEdit{{
					Op:          "upsert",
					Path:        path,
					BlobId:      upload.BlobId,
					ContentHash: upload.ContentHash,
					Mode:        0o100644,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			return ps
		}
		ps1 := up("/acme/payment/app.go", "package payment\nconst V = 1\n")
		ps2 := up("/acme/payment/app.go", "package payment\nconst V = 2\n")
		return cs, ps1, ps2
	}

	csA, ps1A, ps2A := createPatch("allow cli A")
	csB, ps1B, _ := createPatch("allow cli B")

	setupHome := func(token, subjectID string) string {
		home := t.TempDir()
		r := Runner{Home: home}
		if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: token, SubjectID: subjectID}); err != nil {
			t.Fatal(err)
		}
		return home
	}
	runCLI := func(home, dir string, args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		r := Runner{Home: home, Dir: dir, Stdout: &stdout, Stderr: &stderr}
		err := r.Run(ctx, args)
		return stdout.String(), stderr.String(), err
	}

	bobHome := setupHome(bobToken, "user_bob")
	danaHome := setupHome(danaToken, "user_dana")

	// Contributor cannot run gs check allow.
	if _, _, err := runCLI(bobHome, "", "check", "allow", csA.GetId()); err == nil || !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Fatalf("contributor gs check allow err = %v, want permission denied", err)
	}

	// Writer runs gs check allow --patchset <ps1> <cs> in text mode.
	outText, stderr, err := runCLI(danaHome, "", "check", "allow", csA.GetId(), "--patchset", ps1A.GetId())
	if err != nil {
		t.Fatalf("writer gs check allow text failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(outText, "allowed secrets for "+storage.ShortChangesetID(csA.GetId())+" ("+ps1A.GetId()+")") || !strings.Contains(outText, "payment/unit") {
		t.Fatalf("unexpected text output:\n%s", outText)
	}

	// Writer runs gs check allow <cs> --json for current patchset (ps2A).
	outJSON, stderr, err := runCLI(danaHome, "", "check", "allow", csA.GetId(), "--json")
	if err != nil {
		t.Fatalf("writer gs check allow json failed: %v\nstderr:\n%s", err, stderr)
	}
	var got allowPatchsetSecretsOutput
	if err := json.Unmarshal([]byte(outJSON), &got); err != nil {
		t.Fatalf("json parse error: %v\n%s", err, outJSON)
	}
	if got.ChangesetID != csA.GetId() || got.PatchsetID != ps2A.GetId() || got.AllowedBy != "user_dana" || len(got.RerunChecks) != 1 || got.RerunChecks[0].Name != "payment/unit" {
		t.Fatalf("json output = %+v", got)
	}

	// Workspace-default gs check allow without positional arg resolves state.CurrentChangesetID.
	wsDir := t.TempDir()
	gsDir := filepath.Join(wsDir, ".gs")
	if err := os.MkdirAll(gsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(gsDir, "slice.json"), WorkspaceConfig{Account: "acme", Slice: "payment", SliceID: slice.GetId()}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(gsDir, "state.json"), WorkspaceState{CurrentChangesetID: csB.GetId(), CurrentPatchsetID: ps1B.GetId()}, 0o644); err != nil {
		t.Fatal(err)
	}
	wsOut, stderr, err := runCLI(danaHome, wsDir, "check", "allow", "--json")
	if err != nil {
		t.Fatalf("workspace-default gs check allow failed: %v\nstderr:\n%s", err, stderr)
	}
	var wsGot allowPatchsetSecretsOutput
	if err := json.Unmarshal([]byte(wsOut), &wsGot); err != nil {
		t.Fatalf("ws json parse error: %v\n%s", err, wsOut)
	}
	if wsGot.ChangesetID != csB.GetId() || wsGot.AllowedBy != "user_dana" {
		t.Fatalf("ws output = %+v, want cs=%s", wsGot, csB.GetId())
	}

	// Verify gs schema documents gs check allow [changeset].
	schemaOut, stderr, err := runCLI(danaHome, "", "schema")
	if err != nil {
		t.Fatalf("gs schema failed: %v\nstderr:\n%s", err, stderr)
	}
	var doc struct {
		Commands []struct {
			Use           string   `json:"use"`
			Flags         []string `json:"flags"`
			MachineOutput []string `json:"machine_output"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(schemaOut), &doc); err != nil {
		t.Fatalf("schema json error: %v\n%s", err, schemaOut)
	}
	found := false
	for _, cmd := range doc.Commands {
		if cmd.Use == "gs check allow [changeset]" {
			found = true
			if !slices.Contains(cmd.Flags, "--patchset") {
				t.Fatalf("gs check allow flags = %v, missing --patchset", cmd.Flags)
			}
			for _, field := range []string{"changeset", "changeset_id", "patchset_id", "allowed_by", "allowed_at", "rerun_checks"} {
				if !slices.Contains(cmd.MachineOutput, field) {
					t.Fatalf("gs check allow machine_output = %v, missing %s", cmd.MachineOutput, field)
				}
			}
		}
	}
	if !found {
		t.Fatal("gs schema missing command gs check allow [changeset]")
	}
}
