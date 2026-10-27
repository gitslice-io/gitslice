package secretmask

import "testing"

func TestMask(t *testing.T) {
	key := "-----BEGIN KEY-----\nAAAABBBBCCCC\n-----END KEY-----\n"
	m := New([]string{"s3cret-token", "s3cret", "abc", "", "   ", key})
	for in, want := range map[string]string{
		"token=s3cret-token done":           "token=*** done",
		"short s3cret and abc stay?":        "short *** and abc stay?",
		"line AAAABBBBCCCC printed alone":   "line *** printed alone",
		"whole:\n" + key:                    "whole:\n***",
		"nothing secret here":               "nothing secret here",
		"twice s3cret-token, s3cret-token.": "twice ***, ***.",
	} {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNilMaskerMasksNothing(t *testing.T) {
	var m *Masker
	if got := m.Mask("x"); got != "x" {
		t.Fatalf("nil Mask = %q", got)
	}
	if New([]string{"ab", ""}) != nil {
		t.Fatal("New with only short values should be nil")
	}
}
