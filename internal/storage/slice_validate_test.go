package storage

import (
	"errors"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestValidateSliceDefinition(t *testing.T) {
	ref := &corev1.SliceRef{Account: "Nic_1", Slice: "Tools_Core"}
	def, err := ValidateSliceDefinition(ref, &corev1.SliceDefinition{
		IncludedPaths:     []string{"/nic-1/tools", "nic-1/tools"},
		RequiredApprovals: 1,
		RequiredChecks:    []string{"unit", "unit"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.Visibility != "private" || len(def.IncludedPaths) != 1 || def.IncludedPaths[0] != "/nic-1/tools" || len(def.RequiredChecks) != 1 {
		t.Fatalf("unexpected normalized definition: %+v", def)
	}

	cases := []struct {
		name string
		ref  *corev1.SliceRef
		def  *corev1.SliceDefinition
	}{
		{"bad visibility", &corev1.SliceRef{Account: "nic", Slice: "tools"}, &corev1.SliceDefinition{IncludedPaths: []string{"/nic/tools"}, Visibility: "account"}},
		{"other account path", &corev1.SliceRef{Account: "nic", Slice: "tools"}, &corev1.SliceDefinition{IncludedPaths: []string{"/other/tools"}}},
		{"non-home account root", &corev1.SliceRef{Account: "nic", Slice: "tools"}, &corev1.SliceDefinition{IncludedPaths: []string{"/nic"}}},
		{"comma in path", &corev1.SliceRef{Account: "nic", Slice: "tools"}, &corev1.SliceDefinition{IncludedPaths: []string{"/nic/tools,/nic/docs"}}},
		{"comma in check", &corev1.SliceRef{Account: "nic", Slice: "tools"}, &corev1.SliceDefinition{IncludedPaths: []string{"/nic/tools"}, RequiredChecks: []string{"a,b"}}},
		{"negative approvals", &corev1.SliceRef{Account: "nic", Slice: "tools"}, &corev1.SliceDefinition{IncludedPaths: []string{"/nic/tools"}, RequiredApprovals: -1}},
	}
	for _, tc := range cases {
		if _, err := ValidateSliceDefinition(tc.ref, tc.def); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", tc.name, err)
		}
	}

	homeDef, err := ValidateSliceDefinition(&corev1.SliceRef{Account: "nic", Slice: "home"}, &corev1.SliceDefinition{IncludedPaths: []string{"/nic"}})
	if err != nil || len(homeDef.IncludedPaths) != 1 || homeDef.IncludedPaths[0] != "/nic" {
		t.Fatalf("home slice root failed: %+v, err=%v", homeDef, err)
	}
}

func TestCursorCodecs(t *testing.T) {
	ts := time.Date(2026, 11, 27, 9, 10, 11, 123456789, time.UTC)

	tok := EncodeTimeIDCursor(ts, "cs_abc")
	gotTS, gotID, err := DecodeTimeIDCursor(tok)
	if err != nil || !gotTS.Equal(ts) || gotID != "cs_abc" {
		t.Fatalf("TimeIDCursor roundtrip failed: ts=%v id=%q err=%v", gotTS, gotID, err)
	}
	if _, _, err := DecodeTimeIDCursor("not-valid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on bad TimeIDCursor, got %v", err)
	}

	sc := EncodeSliceCursor(true, ts, "home")
	gotSC, ok, err := DecodeSliceCursor(sc)
	if err != nil || !ok || !gotSC.IsHome || !gotSC.Ts.Equal(ts) || gotSC.Slug != "home" {
		t.Fatalf("SliceCursor roundtrip failed: %+v ok=%v err=%v", gotSC, ok, err)
	}

	ct, err := EncodeCommitPageToken("sha256:abc", ts)
	if err != nil {
		t.Fatalf("EncodeCommitPageToken: %v", err)
	}
	gotCT, err := DecodeCommitPageToken(ct)
	if err != nil || gotCT.CommitID != "sha256:abc" {
		t.Fatalf("DecodeCommitPageToken: %+v err=%v", gotCT, err)
	}
}
