package service

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func TestPathIntentsPermissionsScopedKeysAndAutoClear(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := asUser(t, handlers, "user_alice", "alice")
	readerUser := asUser(t, handlers, "user_reader", "readerbob")
	contribUser := asUser(t, handlers, "user_contrib", "contribeve")
	outsider := asUser(t, handlers, "user_outsider", "outsider")
	aliceID, _ := authctx.SubjectID(alice)
	contribID, _ := authctx.SubjectID(contribUser)

	seedPathInMemoryRepo(t, mem, "commit_intents_base", "/team/app/src/billing/tax.go", "package billing\nconst Tax = 1\n")

	if _, err := handlers.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "team"}); err != nil {
		t.Fatal(err)
	}
	slice, err := handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "team", Slice: "app"},
		Visibility:    "private",
		IncludedPaths: []string{"/team/app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sliceRef := slice.GetRef()

	// Grant slice roles: readerbob=reader, contribeve=contributor.
	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice: sliceRef, Username: "readerbob", Role: "reader",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice: sliceRef, Username: "contribeve", Role: "contributor",
	}); err != nil {
		t.Fatal(err)
	}

	// Create delegated agent owned by alice and grant contributor access on team/app.
	agentResp, err := handlers.Auth.CreateDelegatedAgent(alice, &corev1.CreateDelegatedAgentRequest{
		Username: "billing-bot",
		Slice:    sliceRef,
		Role:     "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           agentResp.GetAgentSubjectId(),
		SubjectKind:         "agent",
		ControllerSubjectID: aliceID,
	})

	// Permissions on CreatePathIntent: outsider & reader denied; contributor & agent allowed.
	_, err = handlers.Changeset.CreatePathIntent(outsider, &corev1.CreatePathIntentRequest{
		Slice: sliceRef, Pattern: "/team/app/src/billing/**",
	})
	wantStatus(t, err, codes.PermissionDenied, "outsider CreatePathIntent")

	_, err = handlers.Changeset.CreatePathIntent(readerUser, &corev1.CreatePathIntentRequest{
		Slice: sliceRef, Pattern: "/team/app/src/billing/**",
	})
	wantStatus(t, err, codes.PermissionDenied, "reader CreatePathIntent")

	// Pattern validation: out-of-slice, traversal, negation, and duration bounds rejected.
	for _, badPattern := range []string{"/team/**", "/**", "/team/other/**", "/team/app/../secret", "!/team/app/src/**"} {
		_, err = handlers.Changeset.CreatePathIntent(contribUser, &corev1.CreatePathIntentRequest{
			Slice: sliceRef, Pattern: badPattern,
		})
		wantStatus(t, err, codes.InvalidArgument, "bad pattern "+badPattern)
	}
	for _, badTTL := range []int64{-10, 8 * 24 * 3600, math.MaxInt64} {
		_, err = handlers.Changeset.CreatePathIntent(contribUser, &corev1.CreatePathIntentRequest{
			Slice: sliceRef, Pattern: "/team/app/src/**", TtlSeconds: badTTL,
		})
		wantStatus(t, err, codes.InvalidArgument, "bad ttl")
	}

	// Agent declares intent with trailing slash normalized -> /**, resolved username + controller_username.
	agentIntent, err := handlers.Changeset.CreatePathIntent(agentCtx, &corev1.CreatePathIntentRequest{
		Slice:   sliceRef,
		Pattern: "/team/app/src/billing/",
		Message: "refactoring tax calculation",
	})
	if err != nil {
		t.Fatalf("agent CreatePathIntent: %v", err)
	}
	if agentIntent.GetPattern() != "/team/app/src/billing/**" {
		t.Fatalf("normalized pattern = %q, want /team/app/src/billing/**", agentIntent.GetPattern())
	}
	if agentIntent.GetUsername() != "billing-bot" || agentIntent.GetControllerUsername() != "alice" || agentIntent.GetControllerSubjectId() != aliceID {
		t.Fatalf("unexpected actor resolution on agentIntent: %+v", agentIntent)
	}

	// Contributor also creates an intent on another subtree.
	contribIntent, err := handlers.Changeset.CreatePathIntent(contribUser, &corev1.CreatePathIntentRequest{
		Slice:      sliceRef,
		Pattern:    "/team/app/src/auth/**",
		Message:    "oauth cleanup",
		TtlSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("contrib CreatePathIntent: %v", err)
	}

	// Insert an already-expired intent directly in storage; ListPathIntents must omit it.
	if _, err := mem.Changesets.CreatePathIntent(context.Background(), storage.PathIntent{
		SliceID:   slice.GetId(),
		Account:   "team",
		Slice:     "app",
		SubjectID: contribID,
		Pattern:   "/team/app/expired/**",
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	// Reader can ListPathIntents; outsider cannot; scoped key for wrong slice cannot.
	_, err = handlers.Changeset.ListPathIntents(outsider, &corev1.ListPathIntentsRequest{Slice: sliceRef})
	wantStatus(t, err, codes.PermissionDenied, "outsider ListPathIntents")

	wrongScopeCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:     aliceID,
		SubjectKind:   "user",
		KeyID:         "key_wrong_slice",
		HasSliceScope: true,
		SliceIDs:      []string{"slice_other"},
	})
	_, err = handlers.Changeset.ListPathIntents(wrongScopeCtx, &corev1.ListPathIntentsRequest{Slice: sliceRef})
	wantStatus(t, err, codes.PermissionDenied, "wrong-slice key ListPathIntents")

	listed, err := handlers.Changeset.ListPathIntents(readerUser, &corev1.ListPathIntentsRequest{Slice: sliceRef})
	if err != nil {
		t.Fatalf("reader ListPathIntents: %v", err)
	}
	if len(listed.GetIntents()) != 2 {
		t.Fatalf("ListPathIntents count = %d, want 2: %+v", len(listed.GetIntents()), listed.GetIntents())
	}

	// DeletePathIntent permissions:
	// 1. contribeve cannot delete billing-bot's intent.
	_, err = handlers.Changeset.DeletePathIntent(contribUser, &corev1.DeletePathIntentRequest{IntentId: agentIntent.GetId()})
	wantStatus(t, err, codes.PermissionDenied, "other contributor DeletePathIntent")

	// 2. A reader-capped API key for the creator cannot delete the intent.
	readerKeyCreatorCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:     contribID,
		SubjectKind:   "user",
		KeyID:         "key_reader_cap",
		HasSliceScope: true,
		SliceIDs:      []string{slice.GetId()},
		MaxRole:       "reader",
	})
	_, err = handlers.Changeset.DeletePathIntent(readerKeyCreatorCtx, &corev1.DeletePathIntentRequest{IntentId: contribIntent.GetId()})
	wantStatus(t, err, codes.PermissionDenied, "reader-capped key DeletePathIntent")

	// 3. Human controller (alice) CAN delete billing-bot's intent.
	if _, err := handlers.Changeset.DeletePathIntent(alice, &corev1.DeletePathIntentRequest{IntentId: agentIntent.GetId()}); err != nil {
		t.Fatalf("controller DeletePathIntent: %v", err)
	}

	// 4. Slice/account admin (alice) CAN delete contribeve's intent; writer-capped key CANNOT delete someone else's intent.
	contribIntent2, err := handlers.Changeset.CreatePathIntent(contribUser, &corev1.CreatePathIntentRequest{
		Slice: sliceRef, Pattern: "/team/app/src/auth2/**",
	})
	if err != nil {
		t.Fatal(err)
	}
	writerKeyAdminCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:     aliceID,
		SubjectKind:   "user",
		KeyID:         "key_writer_cap",
		HasSliceScope: true,
		SliceIDs:      []string{slice.GetId()},
		MaxRole:       "writer",
	})
	_, err = handlers.Changeset.DeletePathIntent(writerKeyAdminCtx, &corev1.DeletePathIntentRequest{IntentId: contribIntent2.GetId()})
	wantStatus(t, err, codes.PermissionDenied, "writer-capped key deleting non-owned intent")

	if _, err := handlers.Changeset.DeletePathIntent(alice, &corev1.DeletePathIntentRequest{IntentId: contribIntent2.GetId()}); err != nil {
		t.Fatalf("admin DeletePathIntent: %v", err)
	}
	// Contribeve deletes own intent.
	if _, err := handlers.Changeset.DeletePathIntent(contribUser, &corev1.DeletePathIntentRequest{IntentId: contribIntent.GetId()}); err != nil {
		t.Fatalf("creator DeletePathIntent: %v", err)
	}

	// Auto-link & auto-clear on land and abandon:
	// Give billing-bot writer so alice/bot can submit; test:
	// - overlapIntent: auto-linked on UpdateChangeset and cleared on land
	// - mentionedIntent: mentioned in title by alice, NOT hijacked if mentioned by contribeve!
	overlapIntent, err := handlers.Changeset.CreatePathIntent(alice, &corev1.CreatePathIntentRequest{
		Slice: sliceRef, Pattern: "/team/app/src/billing/**", Message: "land clear test",
	})
	if err != nil {
		t.Fatal(err)
	}
	mentionedIntent, err := handlers.Changeset.CreatePathIntent(alice, &corev1.CreatePathIntentRequest{
		Slice: sliceRef, Pattern: "/team/app/docs/**", Message: "mentioned in description",
	})
	if err != nil {
		t.Fatal(err)
	}
	unrelatedIntent, err := handlers.Changeset.CreatePathIntent(alice, &corev1.CreatePathIntentRequest{
		Slice: sliceRef, Pattern: "/team/app/src/payments/**", Message: "stays active",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Another user mentioning alice's intent ID in an abandoned/landed changeset must NOT clear alice's intent.
	otherCS, err := handlers.Changeset.CreateChangeset(contribUser, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		BaseCommitId:   "commit_intents_base",
		Title:          "Try hijack " + mentionedIntent.GetId(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.AbandonChangeset(contribUser, &corev1.AbandonChangesetRequest{ChangesetId: otherCS.GetId()}); err != nil {
		t.Fatal(err)
	}
	checkStillPresent, err := handlers.Changeset.ListPathIntents(alice, &corev1.ListPathIntentsRequest{Slice: sliceRef})
	if err != nil || len(checkStillPresent.GetIntents()) != 3 {
		t.Fatalf("other user's abandon cleared alice's intent: %+v, err=%v", checkStillPresent.GetIntents(), err)
	}

	// Explicit changeset link cleared on AbandonChangeset.
	abandonCS, err := handlers.Changeset.CreateChangeset(alice, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		BaseCommitId:   "commit_intents_base",
		Title:          "WIP abandon",
	})
	if err != nil {
		t.Fatal(err)
	}
	linkedAbandonIntent, err := handlers.Changeset.CreatePathIntent(alice, &corev1.CreatePathIntentRequest{
		Slice:       sliceRef,
		Pattern:     "/team/app/wip/**",
		ChangesetId: abandonCS.GetId(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.AbandonChangeset(alice, &corev1.AbandonChangesetRequest{ChangesetId: abandonCS.GetId()}); err != nil {
		t.Fatal(err)
	}
	afterAbandon, err := handlers.Changeset.ListPathIntents(alice, &corev1.ListPathIntentsRequest{Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range afterAbandon.GetIntents() {
		if it.GetId() == linkedAbandonIntent.GetId() {
			t.Fatalf("linked intent %s was not cleared on abandon", linkedAbandonIntent.GetId())
		}
	}

	// Alice creates changeset mentioning mentionedIntent and touching /team/app/src/billing/tax.go -> links both & clears on land!
	landCS, err := handlers.Changeset.CreateChangeset(alice, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		BaseCommitId:   "commit_intents_base",
		Title:          "Update tax",
		Description:    "Resolves " + mentionedIntent.GetId(),
	})
	if err != nil {
		t.Fatal(err)
	}
	newContent := []byte("package billing\nconst Tax = 2\n")
	blobResp, err := handlers.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{
		Slice:       sliceRef,
		ContentHash: objectid.RawContentHash(newContent),
		Data:        newContent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.UpdateChangeset(alice, &corev1.UpdateChangesetRequest{
		ChangesetId:  landCS.GetId(),
		BaseCommitId: "commit_intents_base",
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/team/app/src/billing/tax.go",
			BlobId:      blobResp.GetBlobId(),
			ContentHash: blobResp.GetContentHash(),
			Mode:        0o100644,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify overlapIntent and mentionedIntent were linked to landCS before landing.
	beforeLand, err := handlers.Changeset.ListPathIntents(alice, &corev1.ListPathIntentsRequest{Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	linkedCount := 0
	for _, it := range beforeLand.GetIntents() {
		if (it.GetId() == overlapIntent.GetId() || it.GetId() == mentionedIntent.GetId()) && it.GetChangesetId() == landCS.GetId() {
			linkedCount++
		}
	}
	if linkedCount != 2 {
		t.Fatalf("expected 2 intents auto-linked to %s before submit, got %d: %+v", landCS.GetId(), linkedCount, beforeLand.GetIntents())
	}

	if _, err := handlers.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: landCS.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Changesets.PublishPending(context.Background(), 10); err != nil {
		t.Fatal(err)
	}

	afterLand, err := handlers.Changeset.ListPathIntents(alice, &corev1.ListPathIntentsRequest{Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterLand.GetIntents()) != 1 || !strings.EqualFold(afterLand.GetIntents()[0].GetId(), unrelatedIntent.GetId()) {
		t.Fatalf("expected only unrelatedIntent (%s) remaining after land, got: %+v", unrelatedIntent.GetId(), afterLand.GetIntents())
	}
}
