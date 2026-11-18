package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPatchsetApprovedForGitsliceSecretsCatchesDirectoryCopyAndRenameIntoGitslice(t *testing.T) {
	mem, h := newMemoryHandlers()
	ownerCtx := asUser(t, h, "owner_alice", "alice")
	writerCtx := asUser(t, h, "writer_bobby", "bobby")
	writerID, _ := authctx.SubjectID(writerCtx)
	ownerID, _ := authctx.SubjectID(ownerCtx)

	if _, err := h.Auth.CreateOrganization(ownerCtx, &corev1.CreateOrganizationRequest{Slug: "secorg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.InviteAccountMember(ownerCtx, &corev1.InviteAccountMemberRequest{
		Account: "secorg", Username: "bobby", Role: "writer",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.RespondToInvitation(writerCtx, &corev1.RespondToInvitationRequest{
		Account: "secorg", Accept: true,
	}); err != nil {
		t.Fatal(err)
	}

	sliceRef := &corev1.SliceRef{Account: "secorg", Slice: "home"}
	slice, err := mem.Slices.Resolve(context.Background(), sliceRef)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a directory /secorg/templates/bundle containing .gitslice/checks.yaml,
	// and a plain directory /secorg/scripts/hooks containing run.sh.
	data := []byte("version: 1\nchecks: {}\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_sec_seed", []storage.FileEntry{
		{Path: "/secorg/templates/bundle/.gitslice/checks.yaml", BlobID: objectid.BlobID(data), ContentHash: hash, Mode: 0o100644, Size: int64(len(data))},
		{Path: "/secorg/scripts/hooks/run.sh", BlobID: objectid.BlobID(data), ContentHash: hash, Mode: 0o100755, Size: int64(len(data))},
	}, []string{"/secorg/templates/bundle/.gitslice/checks.yaml"})

	dispatcher := &checkDispatcher{
		Auth:        mem.Auth,
		Checks:      mem.Checks,
		Changesets:  mem.Changesets,
		Repository:  mem.Repository,
		Slices:      mem.Slices,
		ObjectStore: mem.Objects,
	}

	// 1. Directory copy where source has nested .gitslice/checks.yaml remapped to /secorg/app/.gitslice/checks.yaml:
	// writer_bob (non-admin) MUST NOT get secrets until an account admin approves or allows!
	csCopy, err := mem.Changesets.Create(context.Background(), writerID, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   "commit_sec_seed",
		Title:          "copy bundle",
	})
	if err != nil {
		t.Fatal(err)
	}
	psCopy, err := mem.Changesets.AddPatchset(context.Background(), csCopy.Id, "", &corev1.Patchset{
		BaseCommitId: "commit_sec_seed",
		Author:       writerID,
		ChangedPaths: []string{"/secorg/app"},
		FileEdits: []*corev1.FileEdit{
			{Op: "copy", OldPath: "/secorg/templates/bundle", Path: "/secorg/app"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.patchsetApprovedForGitsliceSecrets(context.Background(), csCopy, slice, psCopy) {
		t.Fatal("directory copy containing .gitslice subtree must withhold secrets for non-admin writer")
	}

	// Non-admin writer attempting AllowPatchsetSecrets on this directory copy must be rejected with PermissionDenied.
	_, err = h.Check.AllowPatchsetSecrets(writerCtx, &corev1.AllowPatchsetSecretsRequest{
		ChangesetId: csCopy.Id,
		PatchsetId:  psCopy.Id,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("writer AllowPatchsetSecrets on .gitslice dir copy code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}

	// Account owner approving/allowing grants secrets.
	if _, _, err := mem.Checks.AllowPatchsetSecrets(context.Background(), csCopy.Id, psCopy.Id, ownerID); err != nil {
		t.Fatal(err)
	}
	if !dispatcher.patchsetApprovedForGitsliceSecrets(context.Background(), csCopy, slice, psCopy) {
		t.Fatal("account owner secret allowance should approve .gitslice directory copy")
	}

	// 2. Directory rename moving plain /secorg/scripts/hooks into /secorg/app/.gitslice/hooks:
	// writer_bob MUST NOT get secrets without admin approval.
	csRen, err := mem.Changesets.Create(context.Background(), writerID, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   "commit_sec_seed",
		Title:          "rename into .gitslice",
	})
	if err != nil {
		t.Fatal(err)
	}
	psRen, err := mem.Changesets.AddPatchset(context.Background(), csRen.Id, "", &corev1.Patchset{
		BaseCommitId: "commit_sec_seed",
		Author:       writerID,
		ChangedPaths: []string{"/secorg/scripts/hooks"},
		FileEdits: []*corev1.FileEdit{
			{Op: "rename", OldPath: "/secorg/scripts/hooks", Path: "/secorg/app/.gitslice/hooks"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.patchsetApprovedForGitsliceSecrets(context.Background(), csRen, slice, psRen) {
		t.Fatal("directory rename into .gitslice must withhold secrets for non-admin writer")
	}
}
