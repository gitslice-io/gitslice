package service

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func TestScopedAndChildAPIKeysServiceEndToEnd(t *testing.T) {
	ctx := context.Background()
	mem, handlers := newMemoryHandlers()
	owner, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, owner, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.CreateOrganization(ctx, "org", []string{owner}, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_other", ""); err != nil {
		t.Fatal(err)
	}
	other, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_stranger", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, other, "stranger"); err != nil {
		t.Fatal(err)
	}

	sliceAllowed, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "org", Slice: "app"}, []string{"/org/app"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	slicePublic, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "org", Slice: "docs"}, []string{"/org/docs"}, "public", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	sliceSharedDenied, err := mem.Slices.Create(ctx, other, &corev1.SliceRef{Account: "stranger", Slice: "shared"}, []string{"/stranger/shared"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetMember(ctx, sliceSharedDenied.Id, owner, "writer", other); err != nil {
		t.Fatal(err)
	}
	slicePrivateStranger, err := mem.Slices.Create(ctx, other, &corev1.SliceRef{Account: "stranger", Slice: "secret"}, []string{"/stranger/secret"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Put repository files for allowed, public-outside, and empty commits.
	appData := []byte("package app\n")
	appHash := objectid.RawContentHash(appData)
	mem.PutObject(filesystem.BlobKey(appHash), appData)
	docsData := []byte("secret-outside-scope docs\n")
	docsHash := objectid.RawContentHash(docsData)
	mem.PutObject(filesystem.BlobKey(docsHash), docsData)
	commitMixed := "sha256:" + strings.Repeat("1", 64)
	commitApp := "sha256:" + strings.Repeat("2", 64)
	commitDocs := "sha256:" + strings.Repeat("3", 64)
	commitEmpty := "sha256:" + strings.Repeat("4", 64)
	mem.PutCommitWithFiles(commitMixed, []storage.FileEntry{
		{Path: "/org/app/main.go", BlobID: objectid.BlobID(appData), ContentHash: appHash, Mode: 0o100644, Size: int64(len(appData))},
		{Path: "/org/docs/guide.md", BlobID: objectid.BlobID(docsData), ContentHash: docsHash, Mode: 0o100644, Size: int64(len(docsData))},
	}, []string{"/org/app/main.go", "/org/docs/guide.md"})
	mem.PutCommitWithFiles(commitApp, []storage.FileEntry{
		{Path: "/org/app/main.go", BlobID: objectid.BlobID(appData), ContentHash: appHash, Mode: 0o100644, Size: int64(len(appData))},
	}, []string{"/org/app/main.go"})
	mem.PutCommitWithFiles(commitDocs, []storage.FileEntry{
		{Path: "/org/docs/guide.md", BlobID: objectid.BlobID(docsData), ContentHash: docsHash, Mode: 0o100644, Size: int64(len(docsData))},
	}, []string{"/org/docs/guide.md"})
	mem.PutCommitWithFiles(commitEmpty, nil, nil)

	ownerCtx := authctx.WithSubjectID(ctx, owner)
	authCtxForSecret := func(secret string) context.Context {
		p, err := mem.Auth.AuthenticateToken(ctx, secret)
		if err != nil {
			t.Fatalf("AuthenticateToken: %v", err)
		}
		return authctx.WithPrincipal(ctx, *p)
	}

	// Unreadable private slice must return NotFound when minting a scoped key.
	if _, err := handlers.Auth.CreateApiKey(ownerCtx, &corev1.CreateApiKeyRequest{
		Slices: []string{"stranger/secret"},
	}); err == nil {
		t.Fatal("expected error creating key for unreadable private slice")
	} else {
		wantCode(t, err, codes.NotFound, "unreadable slice in CreateApiKey")
	}

	// Mint scoped + role-capped key for owner.
	created, err := handlers.Auth.CreateApiKey(ownerCtx, &corev1.CreateApiKeyRequest{
		Name:             "deploy-app",
		Slices:           []string{"org/app", "org/docs"},
		MaxRole:          "writer",
		ExpiresInSeconds: 3600,
		Purpose:          "ci-runner",
	})
	if err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if len(created.Key.SliceIds) != 2 || len(created.Key.Slices) != 2 ||
		created.Key.Slices[0] != "org/app" || created.Key.Slices[1] != "org/docs" ||
		created.Key.MaxRole != "writer" || created.Key.Purpose != "ci-runner" || created.Key.ExpiresAt == "" {
		t.Fatalf("created key metadata = %+v", created.Key)
	}
	parentKeyCtx := authCtxForSecret(created.Secret)

	// Parent key can attenuate to a subset slice + lower role; records parent_key_id.
	child, err := handlers.Auth.CreateApiKey(parentKeyCtx, &corev1.CreateApiKeyRequest{
		Name:    "app-only-reader",
		Slices:  []string{"org/app"},
		MaxRole: "reader",
		Purpose: "inspect",
	})
	if err != nil {
		t.Fatalf("child CreateApiKey: %v", err)
	}
	if child.Key.ParentKeyId != created.Key.Id || child.Key.MaxRole != "reader" ||
		len(child.Key.Slices) != 1 || child.Key.Slices[0] != "org/app" {
		t.Fatalf("child key metadata = %+v", child.Key)
	}

	// Attempting to widen slice scope or role, or exceed expiry, or target another subject fails.
	_, err = handlers.Auth.CreateApiKey(parentKeyCtx, &corev1.CreateApiKeyRequest{
		Slices: []string{"stranger/shared"},
	})
	wantCode(t, err, codes.PermissionDenied, "widening slice scope beyond parent")
	_, err = handlers.Auth.CreateApiKey(authCtxForSecret(child.Secret), &corev1.CreateApiKeyRequest{
		MaxRole: "writer",
	})
	wantCode(t, err, codes.PermissionDenied, "widening role beyond parent")
	_, err = handlers.Auth.CreateApiKey(parentKeyCtx, &corev1.CreateApiKeyRequest{
		ExpiresInSeconds: 7200,
	})
	wantCode(t, err, codes.PermissionDenied, "extending expiry beyond parent")

	// Scope childCtx to only org/app with reader role.
	childCtx := authCtxForSecret(child.Secret)

	// ListSlices only shows org/app (hides org/docs even though org/docs is public, plus org/home).
	listedSlices, err := handlers.Slice.ListSlices(childCtx, &corev1.ListSlicesRequest{Account: "org"})
	if err != nil {
		t.Fatalf("ListSlices: %v", err)
	}
	if len(listedSlices.Slices) != 1 || listedSlices.Slices[0].Id != sliceAllowed.Id {
		t.Fatalf("scoped ListSlices = %+v, want only %s", listedSlices.Slices, sliceAllowed.Id)
	}
	// ListSharedSlices also filters to scoped slices only.
	sharedResp, err := handlers.Slice.ListSharedSlices(childCtx, &corev1.ListSharedSlicesRequest{})
	if err != nil {
		t.Fatalf("ListSharedSlices: %v", err)
	}
	if len(sharedResp.Slices) != 0 {
		t.Fatalf("scoped ListSharedSlices = %+v, want empty", sharedResp.Slices)
	}

	// ListConversations without req.Slice filters to allowed slices only.
	daemon, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{SubjectID: owner, Account: "org", Name: "d1", Runtime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Agents.CreateConversation(ctx, storage.ConversationInput{DaemonID: daemon.Id, SubjectID: owner, SliceID: sliceAllowed.Id, Account: "org", SliceName: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Agents.CreateConversation(ctx, storage.ConversationInput{DaemonID: daemon.Id, SubjectID: owner, SliceID: slicePublic.Id, Account: "org", SliceName: "docs"}); err != nil {
		t.Fatal(err)
	}
	convs, err := handlers.Agent.ListConversations(childCtx, &corev1.ListConversationsRequest{})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(convs.Conversations) != 1 || convs.Conversations[0].SliceId != sliceAllowed.Id {
		t.Fatalf("scoped ListConversations = %+v, want only %s", convs.Conversations, sliceAllowed.Id)
	}

	// Repository slice-less reads & commit checks enforce scope.
	if _, err := handlers.Repository.ReadFile(childCtx, &corev1.ReadFileRequest{CommitId: commitApp, Path: "/org/app/main.go"}); err != nil {
		t.Fatalf("ReadFile inside scope = %v", err)
	}
	_, err = handlers.Repository.ReadFile(childCtx, &corev1.ReadFileRequest{CommitId: commitDocs, Path: "/org/docs/guide.md"})
	wantCode(t, err, codes.PermissionDenied, "ReadFile out-of-scope public path")
	_, err = handlers.Repository.ListDirectory(childCtx, &corev1.ListDirectoryRequest{CommitId: commitApp, Path: "/org"})
	wantCode(t, err, codes.PermissionDenied, "ListDirectory account folder above scoped subpath")
	rootList, err := handlers.Repository.ListDirectory(childCtx, &corev1.ListDirectoryRequest{CommitId: commitApp, Path: "/"})
	if err != nil {
		t.Fatalf("ListDirectory root = %v", err)
	}
	for _, entry := range rootList.Entries {
		if entry.Name != "org" {
			t.Fatalf("root directory leaked unrelated account %q", entry.Name)
		}
	}
	commits, err := handlers.Repository.ListCommits(childCtx, &corev1.ListCommitsRequest{})
	if err != nil {
		t.Fatalf("ListCommits = %v", err)
	}
	for _, c := range commits.Commits {
		if c.Id == commitDocs {
			t.Fatalf("ListCommits leaked out-of-scope commit %s", c.Id)
		}
	}
	if _, err := handlers.Repository.GetCommit(childCtx, &corev1.GetCommitRequest{CommitId: commitApp}); err != nil {
		t.Fatalf("GetCommit inside scope = %v", err)
	}
	if _, err := handlers.Repository.ResolveCommit(childCtx, &corev1.ResolveCommitRequest{CommitId: commitApp}); err != nil {
		t.Fatalf("ResolveCommit inside scope = %v", err)
	}
	_, err = handlers.Repository.GetCommit(childCtx, &corev1.GetCommitRequest{CommitId: commitMixed})
	wantCode(t, err, codes.PermissionDenied, "GetCommit touching out-of-scope path")
	_, err = handlers.Repository.GetCommit(childCtx, &corev1.GetCommitRequest{CommitId: commitEmpty})
	wantCode(t, err, codes.PermissionDenied, "GetCommit empty commit without changeset")
	_, err = handlers.Repository.ResolveCommit(childCtx, &corev1.ResolveCommitRequest{CommitId: commitDocs})
	wantCode(t, err, codes.NotFound, "ResolveCommit outside scoped prefixes")

	// Account-wide mutations / admin calls denied on restricted keys.
	_, err = handlers.Auth.CreateOrganization(childCtx, &corev1.CreateOrganizationRequest{Slug: "new-org"})
	wantCode(t, err, codes.PermissionDenied, "CreateOrganization")
	_, err = handlers.Auth.ListAccountMembers(childCtx, &corev1.ListAccountMembersRequest{Account: "org"})
	wantCode(t, err, codes.PermissionDenied, "ListAccountMembers")
	_, err = handlers.Auth.SetAccountMember(childCtx, &corev1.SetAccountMemberRequest{Account: "org", Username: "stranger", Role: "reader"})
	wantCode(t, err, codes.PermissionDenied, "SetAccountMember")
	_, err = handlers.Auth.InviteAccountMember(childCtx, &corev1.InviteAccountMemberRequest{Account: "org", Username: "stranger", Role: "reader"})
	wantCode(t, err, codes.PermissionDenied, "InviteAccountMember")
	_, err = handlers.Auth.UpdateAccountProfile(childCtx, &corev1.UpdateAccountProfileRequest{Account: "org", DisplayName: "hack"})
	wantCode(t, err, codes.PermissionDenied, "UpdateAccountProfile org")
	_, err = handlers.Auth.UpdateAccountProfile(childCtx, &corev1.UpdateAccountProfileRequest{Account: "alice", DisplayName: "hack"})
	wantCode(t, err, codes.PermissionDenied, "UpdateAccountProfile personal")
	_, err = handlers.Auth.CompleteCliLogin(childCtx, &corev1.CompleteCliLoginRequest{Code: "x"})
	wantCode(t, err, codes.PermissionDenied, "CompleteCliLogin")

	// Daemon management must reject restricted keys (both reader and contributor),
	// even on a personal daemon owned by the caller.
	personalDaemon, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{SubjectID: owner, Account: "alice", Name: "d-personal", Runtime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Agents.ExposeDaemon(ctx, personalDaemon.Id, slicePublic.Id, owner); err != nil {
		t.Fatal(err)
	}
	contribKey, err := handlers.Auth.CreateApiKey(parentKeyCtx, &corev1.CreateApiKeyRequest{
		Name:    "app-contrib",
		Slices:  []string{"org/app"},
		MaxRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	contribCtx := authCtxForSecret(contribKey.Secret)
	for label, kctx := range map[string]context.Context{"reader": childCtx, "contributor": contribCtx} {
		_, err = handlers.Agent.ExposeDaemon(kctx, &corev1.ExposeDaemonRequest{
			DaemonId: personalDaemon.Id,
			Slice:    &corev1.SliceRef{Account: "org", Slice: "app"},
		})
		wantCode(t, err, codes.PermissionDenied, label+" ExposeDaemon")
		_, err = handlers.Agent.UnexposeDaemon(kctx, &corev1.UnexposeDaemonRequest{
			DaemonId: personalDaemon.Id,
			Slice:    &corev1.SliceRef{Account: "org", Slice: "docs"},
		})
		wantCode(t, err, codes.PermissionDenied, label+" UnexposeDaemon out-of-scope")
		_, err = handlers.Agent.UnexposeDaemon(kctx, &corev1.UnexposeDaemonRequest{
			DaemonId: personalDaemon.Id,
			Slice:    &corev1.SliceRef{Account: "org", Slice: "app"},
		})
		wantCode(t, err, codes.PermissionDenied, label+" UnexposeDaemon in-scope")
		_, err = handlers.Agent.RenameDaemon(kctx, &corev1.RenameDaemonRequest{
			DaemonId:    personalDaemon.Id,
			DisplayName: "hijacked",
		})
		wantCode(t, err, codes.PermissionDenied, label+" RenameDaemon")
		if _, _, mErr := manageableDaemon(kctx, mem.Auth, mem.Agents, owner, personalDaemon.Id); mErr == nil {
			t.Fatalf("%s manageableDaemon unexpectedly succeeded", label)
		}
		listedDaemons, dErr := handlers.Agent.ListDaemons(kctx, &corev1.ListDaemonsRequest{})
		if dErr != nil {
			t.Fatalf("%s ListDaemons: %v", label, dErr)
		}
		for _, d := range listedDaemons.Daemons {
			if d.Manageable {
				t.Fatalf("%s ListDaemons marked %s Manageable=true under restricted key", label, d.Id)
			}
		}
	}

	// Expiry overflow must be rejected with InvalidArgument instead of wrapping int64.
	_, err = handlers.Auth.CreateApiKey(ownerCtx, &corev1.CreateApiKeyRequest{
		Name:             "overflow-exp",
		ExpiresInSeconds: math.MaxInt64/int64(time.Second) + 1,
	})
	wantCode(t, err, codes.InvalidArgument, "CreateApiKey expiry overflow")

	// Controller delegation (authorizeWithDelegation) cannot exceed caller key grant.
	botID := "agent_delegated_bot"
	if err := mem.Auth.CreateOrganization(ctx, "botacct", []string{botID}, botID); err != nil {
		t.Fatal(err)
	}
	botSlice, err := mem.Slices.Create(ctx, botID, &corev1.SliceRef{Account: "botacct", Slice: "work"}, []string{"/botacct/work"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizeWithDelegation(childCtx, mem.Auth, owner, botID, botSlice, authz.ActionRead); err == nil {
		t.Fatalf("expected controller key scoped to [org/app] to fail delegation on botacct/work")
	}

	// Revoking parent cascades to child key.
	listedChild, err := handlers.Auth.ListApiKeys(childCtx, &corev1.ListApiKeysRequest{})
	if err != nil || len(listedChild.Keys) != 1 || listedChild.Keys[0].Id != child.Key.Id {
		t.Fatalf("child ListApiKeys = %+v, %v", listedChild, err)
	}
	if _, err := handlers.Auth.RevokeApiKey(ownerCtx, &corev1.RevokeApiKeyRequest{KeyId: created.Key.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.AuthenticateToken(ctx, child.Secret); err == nil {
		t.Fatal("child key still authenticates after parent revoked")
	}
	_ = slicePrivateStranger
	_ = strings.TrimSpace
}
