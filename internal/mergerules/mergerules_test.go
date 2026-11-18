package mergerules

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAndMatch(t *testing.T) {
	f, err := Parse("/acme/app", []byte("version: 1\nrules:\n  - paths: [\"go.sum\", \"**/go.sum\"]\n    merge: union\n  - paths: [\"**/*.lock\"]\n    merge: newest\n"))
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]Strategy{
		"/acme/app/go.sum":        Union,
		"/acme/app/tools/go.sum":  Union,
		"/acme/app/web/yarn.lock": Newest,
		"/acme/app/main.go":       "",
		"/acme/other/go.sum":      "",
	} {
		got, _ := f.Match(p)
		if got != want {
			t.Errorf("Match(%s) = %q, want %q", p, got, want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, doc := range []string{
		"version: 2\nrules: []\n",
		"version: 1\nrules:\n  - paths: [\"x\"]\n    merge: both\n",
		"version: 1\nrules:\n  - paths: [\"x\"]\n    merge: regenerate:gen\n",
		"version: 1\nrules:\n  - paths: [\"/abs\"]\n    merge: union\n",
		"version: 1\nrules:\n  - merge: union\n",
		"version: 1\nrules:\n  - paths: [\"x\"]\n    merge: union\n    extra: 1\n",
	} {
		if _, err := Parse("/a", []byte(doc)); err == nil {
			t.Errorf("Parse accepted:\n%s", doc)
		}
	}
}

func TestLookupNearestFileWins(t *testing.T) {
	files := map[string]string{
		"/acme/.gitslice/merge.yaml":     "version: 1\nrules:\n  - paths: [\"**/go.sum\"]\n    merge: newest\n",
		"/acme/app/.gitslice/merge.yaml": "version: 1\nrules:\n  - paths: [\"**/*.lock\"]\n    merge: ours\n",
	}
	read := func(p string) ([]byte, bool, error) {
		d, ok := files[p]
		return []byte(d), ok, nil
	}
	// The nearest file exists and has no rule for go.sum: no rule.
	if s, ok, err := Lookup("/acme/app/go.sum", read); err != nil || ok {
		t.Fatalf("Lookup(app/go.sum) = %q %v %v", s, ok, err)
	}
	if s, ok, _ := Lookup("/acme/lib/go.sum", read); !ok || s != Newest {
		t.Fatalf("Lookup(lib/go.sum) = %q %v", s, ok)
	}
	if s, ok, _ := Lookup("/acme/app/x/y.lock", read); !ok || s != Ours {
		t.Fatalf("Lookup(app/x/y.lock) = %q %v", s, ok)
	}
	if got := ConfigPaths("/acme/app/go.sum"); !reflect.DeepEqual(got, []string{"/acme/app/.gitslice/merge.yaml", "/acme/.gitslice/merge.yaml"}) {
		t.Fatalf("ConfigPaths = %v", got)
	}
}

func TestMergeUnion(t *testing.T) {
	base := "a h1\nb h1\nc h1\n"
	ours := "a h1\nc h1\nd h1\n"         // removed b, added d
	theirs := "a h1\nb h1\nc h1\ne h1\n" // added e
	got := string(MergeUnion([]byte(base), []byte(ours), []byte(theirs)))
	if got != "a h1\nc h1\ne h1\nd h1\n" {
		t.Fatalf("MergeUnion = %q", got)
	}
	if !strings.HasSuffix(string(MergeUnion(nil, []byte("x\n"), []byte("y\n"))), "\n") {
		t.Fatal("union should end with a newline")
	}
}
