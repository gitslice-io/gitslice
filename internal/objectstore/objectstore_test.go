package objectstore

import "testing"

func TestBlobKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"sha256:abcdef0123456789", "blobs/sha256/ab/cd/abcdef0123456789"},
		{"abcdef0123456789", "blobs/sha256/ab/cd/abcdef0123456789"},
		{"sha256:abc", "blobs/sha256/abc"},
		{"abc", "blobs/sha256/abc"},
	}
	for _, tc := range cases {
		if got := BlobKey(tc.in); got != tc.want {
			t.Errorf("BlobKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
