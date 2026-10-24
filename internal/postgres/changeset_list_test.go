package postgres

import (
	"errors"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
)

// A summary list is the full list without patchsets: same changesets, same
// order, same fields, including the current patchset's submit requirements.
func TestListChangesetSummaries(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	blobID, hash := upsertTestBlob(t, ctx, store, "package payment\nconst Listed = true\n")
	first := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/listed_one.go", blobID, hash)
	createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/listed_two.go", blobID, hash)
	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	}

	filter := storage.ChangesetListFilter{Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}}
	full, _, err := store.Changesets().List(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	filter.Summary = true
	summaries, _, err := store.Changesets().List(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 2 || len(summaries) != len(full) {
		t.Fatalf("summaries = %d, full = %d", len(summaries), len(full))
	}
	for i := range full {
		if len(summaries[i].Patchsets) != 0 {
			t.Fatalf("summary %s has patchsets", summaries[i].Id)
		}
		want := proto.Clone(full[i]).(*corev1.Changeset)
		want.Patchsets = nil
		if !proto.Equal(summaries[i], want) {
			t.Fatalf("summary %d differs from the full changeset:\n got %v\nwant %v", i, summaries[i], want)
		}
	}
}

func TestListChangesetsFiltersAndPages(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	slice := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	var ids []string
	for _, title := range []string{"Alpha fix", "beta Feature", "Gamma 100% done", "Delta feature two", "Epsilon"} {
		cs, err := store.Changesets().Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
			AuthoringSlice: slice, TargetRef: DefaultTargetRef, BaseCommitId: base.CommitId, Title: title,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, cs.Id)
	}
	if err := store.Changesets().Abandon(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	list := func(filter storage.ChangesetListFilter) ([]string, string) {
		t.Helper()
		filter.Slice = slice
		filter.Summary = true
		got, next, err := store.Changesets().List(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(got))
		for i, cs := range got {
			out[i] = cs.Id
		}
		return out, next
	}

	all, next := list(storage.ChangesetListFilter{})
	if len(all) != 5 || next != "" {
		t.Fatalf("all = %d, next %q", len(all), next)
	}
	// Most recently updated first: the abandoned one was touched last.
	if all[0] != ids[0] {
		t.Fatalf("first = %s, want the changeset updated last (%s)", all[0], ids[0])
	}

	// Pages of two continue where the last ended, with no repeats or gaps.
	var paged []string
	token := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging did not end")
		}
		page, nextToken := list(storage.ChangesetListFilter{Limit: 2, PageToken: token})
		paged = append(paged, page...)
		if nextToken == "" {
			break
		}
		if len(page) != 2 {
			t.Fatalf("a page with a next token has %d changesets, want 2", len(page))
		}
		token = nextToken
	}
	if !slices.Equal(paged, all) {
		t.Fatalf("pages = %v, want %v", paged, all)
	}

	// A page that ends exactly at the end has no next token.
	if page, nextToken := list(storage.ChangesetListFilter{Limit: 5}); len(page) != 5 || nextToken != "" {
		t.Fatalf("exact page: %d, %q", len(page), nextToken)
	}

	if got, _ := list(storage.ChangesetListFilter{Statuses: []string{"abandoned"}}); !slices.Equal(got, []string{ids[0]}) {
		t.Fatalf("abandoned = %v", got)
	}
	if got, _ := list(storage.ChangesetListFilter{Statuses: []string{"draft", "open"}}); len(got) != 4 {
		t.Fatalf("draft or open = %v", got)
	}
	// Filters apply before paging: a short page of a filtered list.
	if got, next := list(storage.ChangesetListFilter{Statuses: []string{"draft"}, Limit: 3}); len(got) != 3 || next == "" {
		t.Fatalf("filtered page = %v, %q", got, next)
	}

	// Text: any case, anywhere in the title; % means itself.
	if got, _ := list(storage.ChangesetListFilter{Query: "FEATURE"}); len(got) != 2 {
		t.Fatalf("feature = %v", got)
	}
	if got, _ := list(storage.ChangesetListFilter{Query: "100%"}); !slices.Equal(got, []string{ids[2]}) {
		t.Fatalf("100%% = %v", got)
	}
	if got, _ := list(storage.ChangesetListFilter{Query: "%"}); !slices.Equal(got, []string{ids[2]}) {
		t.Fatalf("%% alone = %v, want only the title that has one", got)
	}
	// Or the start of an id: the handle in the changeset's link.
	if got, _ := list(storage.ChangesetListFilter{Query: ids[3][:11]}); !slices.Equal(got, []string{ids[3]}) {
		t.Fatalf("id prefix = %v", got)
	}
	if got, _ := list(storage.ChangesetListFilter{Query: ids[3][3:11]}); !slices.Equal(got, []string{ids[3]}) {
		t.Fatalf("id prefix without cs_ = %v", got)
	}

	// Author: one's own changesets; nobody's match none.
	if got, _ := list(storage.ChangesetListFilter{FilterAuthor: true, AuthorSubjectID: "user_alice"}); len(got) != 5 {
		t.Fatalf("alice = %v", got)
	}
	if got, _ := list(storage.ChangesetListFilter{FilterAuthor: true, AuthorSubjectID: "user_bob"}); len(got) != 0 {
		t.Fatalf("bob = %v", got)
	}
	if got, _ := list(storage.ChangesetListFilter{FilterAuthor: true}); len(got) != 0 {
		t.Fatalf("no such author = %v", got)
	}

	if _, _, err := store.Changesets().List(ctx, storage.ChangesetListFilter{Slice: slice, PageToken: "not a token"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad token = %v, want ErrInvalid", err)
	}
}
