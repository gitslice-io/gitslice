package service

import (
	"context"
	"slices"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/proto/core/v1"
)

func TestListChangesetsFiltersAndPages(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	slice := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	mem.PutSlice(slice, []string{"/acme/payment"}, "private")
	mem.AddPersonalAccount("user_alice", "alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, title := range []string{"Alpha fix", "Beta feature", "Gamma"} {
		cs, _ := createStandaloneChangesetWithPatchset(t, ctx, handlers, ref.CommitId, title, "/acme/payment/"+title+".go", "package payment\n")
		ids[title] = cs.Id
	}
	if _, err := handlers.Changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: ids["Gamma"]}); err != nil {
		t.Fatal(err)
	}
	list := func(req *corev1.ListChangesetsRequest) ([]string, string) {
		t.Helper()
		req.AuthoringSlice = slice
		listed, err := handlers.Changeset.ListChangesets(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, cs := range listed.Changesets {
			out = append(out, cs.Title)
		}
		return out, listed.NextPageToken
	}

	if got, next := list(&corev1.ListChangesetsRequest{}); len(got) != 3 || next != "" {
		t.Fatalf("all = %v, next %q", got, next)
	}

	// Status and statuses together; blank names are ignored.
	if got, _ := list(&corev1.ListChangesetsRequest{Status: "abandoned"}); !slices.Equal(got, []string{"Gamma"}) {
		t.Fatalf("status = %v", got)
	}
	if got, _ := list(&corev1.ListChangesetsRequest{Statuses: []string{"abandoned", " ", ""}}); !slices.Equal(got, []string{"Gamma"}) {
		t.Fatalf("statuses = %v", got)
	}
	if got, _ := list(&corev1.ListChangesetsRequest{Status: "abandoned", Statuses: []string{"draft"}}); len(got) != 3 {
		t.Fatalf("status plus statuses = %v", got)
	}

	if got, _ := list(&corev1.ListChangesetsRequest{Query: "BETA"}); !slices.Equal(got, []string{"Beta feature"}) {
		t.Fatalf("query = %v", got)
	}

	// An author by username: the one who wrote them, and a name nobody has.
	if got, _ := list(&corev1.ListChangesetsRequest{Author: "alice"}); len(got) != 3 {
		t.Fatalf("author alice = %v", got)
	}
	if got, _ := list(&corev1.ListChangesetsRequest{Author: "nobody"}); len(got) != 0 {
		t.Fatalf("author nobody = %v, want none", got)
	}

	// Pages continue where the last ended.
	first, token := list(&corev1.ListChangesetsRequest{Limit: 2})
	if len(first) != 2 || token == "" {
		t.Fatalf("first page = %v, token %q", first, token)
	}
	second, last := list(&corev1.ListChangesetsRequest{Limit: 2, PageToken: token})
	if len(second) != 1 || last != "" || slices.Contains(first, second[0]) {
		t.Fatalf("second page = %v, token %q", second, last)
	}
}
