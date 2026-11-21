package service

import (
	"context"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestHomeSliceListTagsAggregatesAccountTags(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	owner := asUser(t, handlers, "user_owner_ext", "owner")

	seedPathInMemoryRepo(t, mem, "commit_1", "/team/api/a.go", "package api\n")
	seedPathInMemoryRepo(t, mem, "commit_2", "/team/web/w.ts", "export const x = 1;\n")
	seedPathInMemoryRepo(t, mem, "commit_3", "/team/legacy/l.go", "package legacy\n")
	seedPathInMemoryRepo(t, mem, "commit_other", "/other/app/o.go", "package other\n")

	if _, err := handlers.Auth.CreateOrganization(owner, &corev1.CreateOrganizationRequest{Slug: "team"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.CreateOrganization(owner, &corev1.CreateOrganizationRequest{Slug: "other"}); err != nil {
		t.Fatal(err)
	}

	homeRef := &corev1.SliceRef{Account: "team", Slice: "home"}
	apiSlice, err := handlers.Slice.CreateSlice(owner, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "team", Slice: "api"},
		Visibility:    "public",
		IncludedPaths: []string{"/team/api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	webSlice, err := handlers.Slice.CreateSlice(owner, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "team", Slice: "web"},
		Visibility:    "private",
		IncludedPaths: []string{"/team/web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	legacySlice, err := handlers.Slice.CreateSlice(owner, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "team", Slice: "legacy"},
		Visibility:    "private",
		IncludedPaths: []string{"/team/legacy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherSlice, err := handlers.Slice.CreateSlice(owner, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "other", Slice: "app"},
		Visibility:    "public",
		IncludedPaths: []string{"/other/app"},
	})
	if err != nil {
		t.Fatal(err)
	}

	mustTag := func(ref *corev1.SliceRef, name, commitID string) {
		t.Helper()
		if _, err := handlers.Slice.CreateTag(owner, &corev1.CreateTagRequest{
			Slice:    ref,
			Name:     name,
			CommitId: commitID,
		}); err != nil {
			t.Fatalf("CreateTag(%s/%s, %s): %v", ref.Account, ref.Slice, name, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Same commit, different names across slices: home should display both.
	mustTag(apiSlice.Ref, "api-v1", "commit_1")
	mustTag(webSlice.Ref, "web-v1", "commit_1")

	// Same tag name across two sub-slices: newest (web) wins in home.
	mustTag(apiSlice.Ref, "v1.0.0", "commit_1")
	mustTag(webSlice.Ref, "v1.0.0", "commit_2")

	// Same tag name on home and a newer sub-slice: home's own tag still wins.
	mustTag(homeRef, "v2.0.0", "commit_1")
	mustTag(apiSlice.Ref, "v2.0.0", "commit_2")

	// Tag on a slice that is later archived: still included in home.
	mustTag(legacySlice.Ref, "legacy-v0", "commit_3")
	if _, err := handlers.Slice.SetSliceArchived(owner, &corev1.SetSliceArchivedRequest{Slice: legacySlice.Ref, Archived: true}); err != nil {
		t.Fatalf("SetSliceArchived: %v", err)
	}

	// Tag in another account: must never appear in team/home.
	mustTag(otherSlice.Ref, "other-v1", "commit_other")

	resp, err := handlers.Slice.ListTags(owner, &corev1.ListTagsRequest{Slice: homeRef})
	if err != nil {
		t.Fatalf("ListTags(team/home): %v", err)
	}
	gotByName := map[string]*corev1.Tag{}
	for _, tag := range resp.Tags {
		if _, dup := gotByName[tag.Name]; dup {
			t.Fatalf("duplicate tag name %q in home ListTags", tag.Name)
		}
		gotByName[tag.Name] = tag
	}

	want := map[string]struct {
		slice    string
		commitID string
	}{
		"api-v1":    {slice: "api", commitID: "commit_1"},
		"web-v1":    {slice: "web", commitID: "commit_1"},
		"v1.0.0":    {slice: "web", commitID: "commit_2"},
		"v2.0.0":    {slice: "home", commitID: "commit_1"},
		"legacy-v0": {slice: "legacy", commitID: "commit_3"},
	}
	if len(gotByName) != len(want) {
		t.Fatalf("home tags = %v, want %d tags", gotByName, len(want))
	}
	for name, exp := range want {
		tag, ok := gotByName[name]
		if !ok {
			t.Fatalf("missing tag %q in home ListTags", name)
		}
		if tag.GetSlice().GetSlice() != exp.slice {
			t.Errorf("tag %q slice = %q, want %q", name, tag.GetSlice().GetSlice(), exp.slice)
		}
		if tag.GetCommitId() != exp.commitID {
			t.Errorf("tag %q commit = %q, want %q", name, tag.GetCommitId(), exp.commitID)
		}
	}

	// Non-home slice does not aggregate sibling or home tags.
	apiResp, err := handlers.Slice.ListTags(owner, &corev1.ListTagsRequest{Slice: apiSlice.Ref})
	if err != nil {
		t.Fatalf("ListTags(team/api): %v", err)
	}
	if len(apiResp.Tags) != 3 {
		t.Fatalf("team/api tag count = %d, want 3 (api-v1, v1.0.0, v2.0.0)", len(apiResp.Tags))
	}

	// Make team/home public and verify anonymous caller only sees tags from
	// public slices (home + api), not private slices (web, legacy).
	homeSlice, err := handlers.Slice.ResolveSlice(owner, &corev1.ResolveSliceRequest{Ref: homeRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Slice.UpdateSliceDefinition(owner, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                homeSlice.Id,
		ExpectedDefinitionHash: homeSlice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: homeSlice.Definition.IncludedPaths,
			Visibility:    "public",
		},
	}); err != nil {
		t.Fatalf("UpdateSliceDefinition(team/home public): %v", err)
	}

	anonResp, err := handlers.Slice.ListTags(context.Background(), &corev1.ListTagsRequest{Slice: homeRef})
	if err != nil {
		t.Fatalf("anonymous ListTags(team/home): %v", err)
	}
	anonByName := map[string]*corev1.Tag{}
	for _, tag := range anonResp.Tags {
		anonByName[tag.Name] = tag
	}
	for _, forbidden := range []string{"web-v1", "legacy-v0", "other-v1"} {
		if _, leaked := anonByName[forbidden]; leaked {
			t.Errorf("anonymous ListTags(team/home) leaked private/foreign tag %q", forbidden)
		}
	}
	if anonByName["api-v1"].GetCommitId() != "commit_1" {
		t.Errorf("anonymous api-v1 = %+v, want commit_1", anonByName["api-v1"])
	}
	// With web hidden from anonymous caller, api's v1.0.0 surfaces instead.
	if anonByName["v1.0.0"].GetSlice().GetSlice() != "api" || anonByName["v1.0.0"].GetCommitId() != "commit_1" {
		t.Errorf("anonymous v1.0.0 = %+v, want api@commit_1", anonByName["v1.0.0"])
	}
	if anonByName["v2.0.0"].GetSlice().GetSlice() != "home" || anonByName["v2.0.0"].GetCommitId() != "commit_1" {
		t.Errorf("anonymous v2.0.0 = %+v, want home@commit_1", anonByName["v2.0.0"])
	}

	// External user shared on web (private) sees home + api (public) + web (shared),
	// but not legacy (private, unshared).
	guest := asUser(t, handlers, "user_guest_ext", "guest")
	if _, err := handlers.Slice.SetSliceMember(owner, &corev1.SetSliceMemberRequest{
		Slice: webSlice.Ref, Username: "guest", Role: "reader",
	}); err != nil {
		t.Fatalf("SetSliceMember(web, guest): %v", err)
	}
	guestResp, err := handlers.Slice.ListTags(guest, &corev1.ListTagsRequest{Slice: homeRef})
	if err != nil {
		t.Fatalf("guest ListTags(team/home): %v", err)
	}
	guestByName := map[string]*corev1.Tag{}
	for _, tag := range guestResp.Tags {
		guestByName[tag.Name] = tag
	}
	if _, ok := guestByName["web-v1"]; !ok {
		t.Errorf("guest missing shared web-v1 tag: %+v", guestByName)
	}
	if _, leaked := guestByName["legacy-v0"]; leaked {
		t.Errorf("guest leaked unshared private legacy-v0 tag")
	}

	// Slice-scoped API key for the owner restricted to home + api must not see
	// tags from web or legacy.
	ownerID, _ := authctx.SubjectID(owner)
	scopedOwner := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:     ownerID,
		KeyID:         "key_scoped_home_api",
		HasSliceScope: true,
		SliceIDs:      []string{homeSlice.Id, apiSlice.Id},
	})
	scopedResp, err := handlers.Slice.ListTags(scopedOwner, &corev1.ListTagsRequest{Slice: homeRef})
	if err != nil {
		t.Fatalf("scoped ListTags(team/home): %v", err)
	}
	scopedByName := map[string]*corev1.Tag{}
	for _, tag := range scopedResp.Tags {
		scopedByName[tag.Name] = tag
	}
	for _, forbidden := range []string{"web-v1", "legacy-v0"} {
		if _, leaked := scopedByName[forbidden]; leaked {
			t.Errorf("scoped key leaked out-of-scope tag %q", forbidden)
		}
	}
	if scopedByName["api-v1"].GetCommitId() != "commit_1" || scopedByName["v2.0.0"].GetSlice().GetSlice() != "home" {
		t.Errorf("scoped key tags = %+v, want api-v1 and home v2.0.0", scopedByName)
	}
}
