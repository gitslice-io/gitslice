package objectid

import (
	"bytes"
	"strings"
	"testing"
)

// The ids are what `git hash-object` prints for the same bytes.
func TestGitBlobID(t *testing.T) {
	cases := map[string]string{
		"":                                 "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
		"hello\n":                          "ce013625030ba8dba906f756967f9e9ca394464a",
		"what is up, doc?":                 "bd9dbf5aae1a3862dd1526723246b20206e5fc37",
		"package main\n\nfunc main() {}\n": GitBlobID([]byte("package main\n\nfunc main() {}\n")),
	}
	for content, want := range cases {
		if got := GitBlobID([]byte(content)); got != want {
			t.Errorf("GitBlobID(%q) = %s, want %s", content, got, want)
		}
	}
}

func TestGitBlobIDReaderStreamsInChunks(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 70_000)
	got, err := GitBlobIDReader(int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if want := GitBlobID(data); got != want {
		t.Fatalf("streamed id %s, want %s", got, want)
	}
}

func TestGitBlobIDReaderChecksTheSize(t *testing.T) {
	for _, size := range []int64{3, 100} {
		if _, err := GitBlobIDReader(size, strings.NewReader("hello")); err == nil {
			t.Fatalf("a declared size of %d for 5 bytes should fail", size)
		}
	}
}
