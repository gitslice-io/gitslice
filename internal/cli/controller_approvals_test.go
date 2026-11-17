package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestSliceCLIRequiredHumanApprovalsAndCSStatus(t *testing.T) {
	ctx := context.Background()
	const aliceToken = "alice-cli-token"
	serverAddr, mem := startAgentSignupServerWithStores(t, false, map[string]string{
		aliceToken: "user_alice",
	})

	if _, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_alice", "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	mem.AddPersonalAccount("user_alice", "alice")
	mem.AddAccountRole("user_alice", "acme", "owner")
	seedData := []byte("seed\n")
	seedHash := objectid.RawContentHash(seedData)
	mem.PutObject(filesystem.BlobKey(seedHash), seedData)
	mem.PutCommitWithFiles("commit_cli_human_seed", []storage.FileEntry{{
		Path:        "/acme/cli-human/seed.txt",
		BlobID:      objectid.BlobID(seedData),
		ContentHash: seedHash,
		Mode:        0o100644,
		Size:        int64(len(seedData)),
	}}, []string{"/acme/cli-human/seed.txt"})

	runCLI := func(home string, args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		r := Runner{Home: home, Stdout: &stdout, Stderr: &stderr}
		err := r.Run(ctx, args)
		return stdout.String(), stderr.String(), err
	}

	home := t.TempDir()
	setupRunner := Runner{Home: home}
	if err := setupRunner.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      aliceToken,
		SubjectID:  "user_alice",
	}); err != nil {
		t.Fatal(err)
	}

	// Reject negative --required-human-approvals on create.
	if _, _, err := runCLI(home, "slice", "create", "acme:cli-bad", "--include", "/acme/cli-human", "--required-human-approvals", "-1"); err == nil || !strings.Contains(err.Error(), "required human approvals must be zero or greater") {
		t.Fatalf("slice create negative err = %v, want validation error", err)
	}

	// Create slice with --required-approvals 2 --required-human-approvals 1 --json.
	out, stderr, err := runCLI(home, "slice", "create", "acme:cli-human", "--include", "/acme/cli-human", "--required-approvals", "2", "--required-human-approvals", "1", "--json")
	if err != nil {
		t.Fatalf("slice create failed: %v\nstderr:\n%s", err, stderr)
	}
	var created sliceOutput
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("create json: %v\n%s", err, out)
	}
	if created.RequiredApprovals != 2 || created.RequiredHumanApprovals != 1 {
		t.Fatalf("created = %+v, want required_approvals=2 required_human_approvals=1", created)
	}

	// slice info reports required_human_approvals in both text and JSON.
	infoText, stderr, err := runCLI(home, "slice", "info", "acme:cli-human")
	if err != nil {
		t.Fatalf("slice info text failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(infoText, "required_human_approvals: 1") {
		t.Fatalf("slice info text missing required_human_approvals: 1:\n%s", infoText)
	}
	infoJSON, stderr, err := runCLI(home, "slice", "info", "acme:cli-human", "--json")
	if err != nil {
		t.Fatalf("slice info json failed: %v\nstderr:\n%s", err, stderr)
	}
	var infoOut sliceOutput
	if err := json.Unmarshal([]byte(infoJSON), &infoOut); err != nil {
		t.Fatalf("info json: %v\n%s", err, infoJSON)
	}
	if infoOut.RequiredHumanApprovals != 1 {
		t.Fatalf("infoOut.RequiredHumanApprovals = %d, want 1", infoOut.RequiredHumanApprovals)
	}

	// slice list reports required human approvals when > 0.
	listText, stderr, err := runCLI(home, "slice", "list", "acme")
	if err != nil {
		t.Fatalf("slice list failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(listText, "required human approvals: 1") {
		t.Fatalf("slice list text missing required human approvals: 1:\n%s", listText)
	}

	// Reject negative --required-human-approvals on update.
	if _, _, err := runCLI(home, "slice", "update", "acme:cli-human", "--required-human-approvals", "-1"); err == nil || !strings.Contains(err.Error(), "required human approvals must be zero or greater") {
		t.Fatalf("slice update negative err = %v, want validation error", err)
	}

	// Update --required-human-approvals to 2 and verify info + history.
	updOut, stderr, err := runCLI(home, "slice", "update", "acme:cli-human", "--required-human-approvals", "2", "--json")
	if err != nil {
		t.Fatalf("slice update failed: %v\nstderr:\n%s", err, stderr)
	}
	var updated sliceOutput
	if err := json.Unmarshal([]byte(updOut), &updated); err != nil {
		t.Fatalf("update json: %v\n%s", err, updOut)
	}
	if updated.RequiredHumanApprovals != 2 {
		t.Fatalf("updated.RequiredHumanApprovals = %d, want 2", updated.RequiredHumanApprovals)
	}

	histOut, stderr, err := runCLI(home, "slice", "history", "acme:cli-human", "--json")
	if err != nil {
		t.Fatalf("slice history failed: %v\nstderr:\n%s", err, stderr)
	}
	var histPayload struct {
		Versions []sliceDefinitionVersionOutput `json:"versions"`
	}
	if err := json.Unmarshal([]byte(histOut), &histPayload); err != nil {
		t.Fatalf("history json: %v\n%s", err, histOut)
	}
	if len(histPayload.Versions) != 2 || histPayload.Versions[0].RequiredHumanApprovals != 2 || histPayload.Versions[1].RequiredHumanApprovals != 1 {
		t.Fatalf("versions = %+v, want [2, 1]", histPayload.Versions)
	}

	// Draft changeset against acme:cli-human and inspect gs cs status output.
	ref, err := mem.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mem.Changesets.Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "cli-human"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "cli human gate cs",
	})
	if err != nil {
		t.Fatal(err)
	}
	fileData := []byte("updated\n")
	fileBlobID := objectid.BlobID(fileData)
	fileHash := objectid.RawContentHash(fileData)
	fileKey := filesystem.BlobKey(fileHash)
	if err := mem.Objects.Put(ctx, fileKey, bytes.NewReader(fileData)); err != nil {
		t.Fatal(err)
	}
	if err := mem.Blobs.Upsert(ctx, fileBlobID, fileHash, int64(len(fileData)), fileKey); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Changesets.AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: ref.CommitId,
		Author:       "user_alice",
		ChangedPaths: []string{"/acme/cli-human/new.txt"},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/cli-human/new.txt",
			BlobId:      fileBlobID,
			ContentHash: fileHash,
			Mode:        0o100644,
		}},
		PathBases: []*corev1.PathBase{{
			Path:             "/acme/cli-human/new.txt",
			BaseCommitId:     ref.CommitId,
			Exists:           false,
			EntryFingerprint: storage.MissingEntryFingerprint(),
			Check:            "entry_fingerprint",
		}},
		ReadSet:  []*corev1.PathSetEntry{{Path: "/acme/cli-human/new.txt"}},
		WriteSet: []*corev1.PathSetEntry{{Path: "/acme/cli-human/new.txt"}},
	}); err != nil {
		t.Fatal(err)
	}

	csStatusText, stderr, err := runCLI(home, "cs", "status", cs.Id)
	if err != nil {
		t.Fatalf("cs status text failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(csStatusText, "required_human_approvals: 2") {
		t.Fatalf("cs status missing required_human_approvals: 2:\n%s", csStatusText)
	}

	// Verify gs schema exposes --required-human-approvals and required_human_approvals machine output.
	schemaOut, stderr, err := runCLI(home, "schema")
	if err != nil {
		t.Fatalf("schema failed: %v\nstderr:\n%s", err, stderr)
	}
	var doc struct {
		Commands []struct {
			Use           string   `json:"use"`
			Flags         []string `json:"flags"`
			MachineOutput []string `json:"machine_output"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(schemaOut), &doc); err != nil {
		t.Fatalf("schema json: %v\n%s", err, schemaOut)
	}
	seen := map[string]bool{}
	for _, cmd := range doc.Commands {
		switch {
		case strings.HasPrefix(cmd.Use, "gs slice create "):
			seen["gs slice create"] = true
			if !slices.Contains(cmd.Flags, "--required-human-approvals") {
				t.Fatalf("%s flags = %v, missing --required-human-approvals", cmd.Use, cmd.Flags)
			}
			if !slices.Contains(cmd.MachineOutput, "required_human_approvals") {
				t.Fatalf("%s machine_output = %v, missing required_human_approvals", cmd.Use, cmd.MachineOutput)
			}
		case strings.HasPrefix(cmd.Use, "gs slice update "):
			seen["gs slice update"] = true
			if !slices.Contains(cmd.Flags, "--required-human-approvals") {
				t.Fatalf("%s flags = %v, missing --required-human-approvals", cmd.Use, cmd.Flags)
			}
			if !slices.Contains(cmd.MachineOutput, "required_human_approvals") {
				t.Fatalf("%s machine_output = %v, missing required_human_approvals", cmd.Use, cmd.MachineOutput)
			}
		case strings.HasPrefix(cmd.Use, "gs slice info "):
			seen["gs slice info"] = true
			if !slices.Contains(cmd.MachineOutput, "required_human_approvals") {
				t.Fatalf("%s machine_output = %v, missing required_human_approvals", cmd.Use, cmd.MachineOutput)
			}
		}
	}
	for _, name := range []string{"gs slice create", "gs slice info", "gs slice update"} {
		if !seen[name] {
			t.Fatalf("schema missing command %q", name)
		}
	}
}
