package storage

import (
	"testing"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestShortChangesetID(t *testing.T) {
	for _, tt := range []struct {
		name string
		id   string
		want string
	}{
		{
			name: "full id",
			id:   "cs_3f9a2b1c4d5e6f708192a3b4c5d6e7f8",
			want: "3f9a2b1c4d",
		},
		{
			name: "hex body",
			id:   "3f9a2b1c4d5e6f708192a3b4c5d6e7f8",
			want: "3f9a2b1c4d",
		},
		{
			name: "already short",
			id:   "3f9a2b1c",
			want: "3f9a2b1c",
		},
		{
			name: "empty",
			id:   "",
			want: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShortChangesetID(tt.id); got != tt.want {
				t.Fatalf("ShortChangesetID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestChangesetIDLookupPrefix(t *testing.T) {
	for _, tt := range []struct {
		name     string
		selector string
		want     string
		wantOK   bool
	}{
		{
			name:     "full id",
			selector: "cs_3f9a2b1c4d5e6f708192a3b4c5d6e7f8",
			want:     "cs_3f9a2b1c4d5e6f708192a3b4c5d6e7f8",
			wantOK:   true,
		},
		{
			name:     "short prefix",
			selector: "3f9a2b1c4d",
			want:     "cs_3f9a2b1c4d",
			wantOK:   true,
		},
		{
			name:     "short prefix with id prefix",
			selector: "cs_3f9a2b1c4d",
			want:     "cs_3f9a2b1c4d",
			wantOK:   true,
		},
		{
			name:     "uppercase",
			selector: "CS_3F9A2B1C4D",
			want:     "cs_3f9a2b1c4d",
			wantOK:   true,
		},
		{
			name:     "minimum length",
			selector: "3f9a",
			want:     "cs_3f9a",
			wantOK:   true,
		},
		{
			name:     "too short",
			selector: "3f9",
			wantOK:   false,
		},
		{
			name:     "too long",
			selector: "3f9a2b1c4d5e6f708192a3b4c5d6e7f80",
			wantOK:   false,
		},
		{
			name:     "non hex",
			selector: "3f9a2b1c4g",
			wantOK:   false,
		},
		{
			name:     "handle",
			selector: "alice:first-one@4",
			wantOK:   false,
		},
		{
			name:     "empty",
			selector: "",
			wantOK:   false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ChangesetIDLookupPrefix(tt.selector)
			if ok != tt.wantOK {
				t.Fatalf("ChangesetIDLookupPrefix(%q) ok = %t, want %t", tt.selector, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("ChangesetIDLookupPrefix(%q) = %q, want %q", tt.selector, got, tt.want)
			}
		})
	}
}

func TestPopulateChangesetHandlesLeavesDeprecatedFieldsEmpty(t *testing.T) {
	cs := &corev1.Changeset{
		Id:             "cs_3f9a2b1c4d5e6f708192a3b4c5d6e7f8",
		AuthoringSlice: &corev1.SliceRef{Account: "alice", Slice: "first-one"},
		Number:         4,
		Patchsets: []*corev1.Patchset{
			{Id: "ps_1", Number: 1},
		},
	}

	PopulateChangesetHandles(cs)

	if cs.Handle != "" {
		t.Fatalf("changeset handle populated: %q", cs.Handle)
	}
	if got := cs.Patchsets[0].Handle; got != "" {
		t.Fatalf("patchset handle populated: %q", got)
	}
}
