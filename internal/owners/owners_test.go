package owners

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseAndMatch(t *testing.T) {
	doc := `
version: 1
owners:
  - paths: ["api/**"]
    owners: ["@acme/api-agents", "Alice", "alice"]
  - paths: ["web/**", "*.md"]
    owners: ["web-agent", "bob"]
    require: 2
  - paths: ["docs/generated/**"]
    owners: ["docs-bot"]
    require: 0
`
	f, err := Parse("/acme/app", []byte(doc))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	if len(f.Rules) != 3 {
		t.Fatalf("len(Rules) = %d, want 3", len(f.Rules))
	}
	if f.Rules[0].Require != 1 {
		t.Fatalf("default require = %d, want 1", f.Rules[0].Require)
	}
	if !reflect.DeepEqual(f.Rules[0].Owners, []string{"@acme/api-agents", "alice"}) {
		t.Fatalf("Rules[0].Owners = %v", f.Rules[0].Owners)
	}
	if f.Rules[1].Require != 2 {
		t.Fatalf("Rules[1].Require = %d, want 2", f.Rules[1].Require)
	}
	if f.Rules[2].Require != 0 {
		t.Fatalf("Rules[2].Require = %d, want 0", f.Rules[2].Require)
	}

	tests := []struct {
		path      string
		wantOK    bool
		wantIdx   int
		wantReq   int
		wantOwner []string
	}{
		{
			path:      "/acme/app/api/v1/users.go",
			wantOK:    true,
			wantIdx:   0,
			wantReq:   1,
			wantOwner: []string{"@acme/api-agents", "alice"},
		},
		{
			path:      "/acme/app/web/index.tsx",
			wantOK:    true,
			wantIdx:   1,
			wantReq:   2,
			wantOwner: []string{"web-agent", "bob"},
		},
		{
			path:      "/acme/app/README.md",
			wantOK:    true,
			wantIdx:   1,
			wantReq:   2,
			wantOwner: []string{"web-agent", "bob"},
		},
		{
			path:      "/acme/app/docs/generated/api.html",
			wantOK:    true,
			wantIdx:   2,
			wantReq:   0,
			wantOwner: []string{"docs-bot"},
		},
		{
			path:   "/acme/app/cmd/main.go",
			wantOK: false,
		},
		{
			path:   "/acme/other/api/v1/users.go",
			wantOK: false,
		},
		{
			path:   "/acme/app",
			wantOK: false,
		},
		{
			path:   "/acme/app2/api/users.go",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		rule, idx, ok := f.MatchIndex(tc.path)
		if ok != tc.wantOK {
			t.Errorf("MatchIndex(%s) ok = %v, want %v", tc.path, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if idx != tc.wantIdx || rule.Require != tc.wantReq || !reflect.DeepEqual(rule.Owners, tc.wantOwner) {
			t.Errorf("MatchIndex(%s) = (idx=%d req=%d owners=%v), want (idx=%d req=%d owners=%v)",
				tc.path, idx, rule.Require, rule.Owners, tc.wantIdx, tc.wantReq, tc.wantOwner)
		}
		r2, ok2 := f.Match(tc.path)
		if !ok2 || !reflect.DeepEqual(r2, rule) {
			t.Errorf("Match(%s) = (%+v, %v), want (%+v, true)", tc.path, r2, ok2, rule)
		}
	}
}

func TestParseFirstMatchingRuleWins(t *testing.T) {
	doc := `
version: 1
owners:
  - paths: ["api/payments/**"]
    owners: ["@acme/payments"]
    require: 2
  - paths: ["api/**"]
    owners: ["@acme/api"]
    require: 1
`
	f, err := Parse("/acme/app", []byte(doc))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	rule, idx, ok := f.MatchIndex("/acme/app/api/payments/charge.go")
	if !ok || idx != 0 || rule.Require != 2 || !reflect.DeepEqual(rule.Owners, []string{"@acme/payments"}) {
		t.Fatalf("MatchIndex = (%+v, %d, %v)", rule, idx, ok)
	}
}

func TestParseEmptyOwnersListAllowed(t *testing.T) {
	f, err := Parse("/acme/app", []byte("version: 1\nowners: []\n"))
	if err != nil {
		t.Fatalf("Parse empty owners list error = %v", err)
	}
	if _, ok := f.Match("/acme/app/main.go"); ok {
		t.Fatal("expected no match for empty owners list")
	}
}

func TestParseRefuses(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{name: "empty", doc: ""},
		{name: "missing version", doc: "owners: []\n"},
		{name: "wrong version", doc: "version: 2\nowners: []\n"},
		{name: "unknown field", doc: "version: 1\nowners:\n  - paths: [\"x\"]\n    owners: [\"alice\"]\n    extra: 1\n"},
		{name: "negative require", doc: "version: 1\nowners:\n  - paths: [\"x\"]\n    owners: [\"alice\"]\n    require: -1\n"},
		{name: "empty paths", doc: "version: 1\nowners:\n  - paths: []\n    owners: [\"alice\"]\n"},
		{name: "missing paths", doc: "version: 1\nowners:\n  - owners: [\"alice\"]\n"},
		{name: "empty pattern element", doc: "version: 1\nowners:\n  - paths: [\"  \"]\n    owners: [\"alice\"]\n"},
		{name: "empty owners", doc: "version: 1\nowners:\n  - paths: [\"api/**\"]\n    owners: []\n"},
		{name: "missing owners", doc: "version: 1\nowners:\n  - paths: [\"api/**\"]\n"},
		{name: "blank owner element", doc: "version: 1\nowners:\n  - paths: [\"api/**\"]\n    owners: [\"  \"]\n"},
		{name: "leading slash glob", doc: "version: 1\nowners:\n  - paths: [\"/api/**\"]\n    owners: [\"alice\"]\n"},
		{name: "parent escape glob", doc: "version: 1\nowners:\n  - paths: [\"../secret/**\"]\n    owners: [\"alice\"]\n"},
		{name: "nested parent escape", doc: "version: 1\nowners:\n  - paths: [\"api/../secret/**\"]\n    owners: [\"alice\"]\n"},
		{name: "backslash parent escape", doc: "version: 1\nowners:\n  - paths: [\"..\\secret\"]\n    owners: [\"alice\"]\n"},
		{name: "invalid doublestar pattern", doc: "version: 1\nowners:\n  - paths: [\"api/[unclosed\"]\n    owners: [\"alice\"]\n"},
		{name: "control char in pattern", doc: "version: 1\nowners:\n  - paths: [\"api/\\nfoo\"]\n    owners: [\"alice\"]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse("/acme/app", []byte(tc.doc)); err == nil {
				t.Fatalf("Parse accepted invalid %s:\n%s", tc.name, tc.doc)
			}
		})
	}
}

func TestConfigPaths(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{
			in: "/acme/app/api/users.go",
			want: []string{
				"/acme/app/api/.gitslice/owners.yaml",
				"/acme/app/.gitslice/owners.yaml",
				"/acme/.gitslice/owners.yaml",
			},
		},
		{
			in:   "/acme/README.md",
			want: []string{"/acme/.gitslice/owners.yaml"},
		},
		{
			in:   "acme/app/main.go",
			want: []string{"/acme/app/.gitslice/owners.yaml", "/acme/.gitslice/owners.yaml"},
		},
		{in: "/acme", want: nil},
		{in: "/", want: nil},
		{in: "", want: nil},
	}
	for _, tc := range cases {
		got := ConfigPaths(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ConfigPaths(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLookupNearestFileWins(t *testing.T) {
	files := map[string]string{
		"/acme/.gitslice/owners.yaml": `
version: 1
owners:
  - paths: ["**/*.go"]
    owners: ["@acme/platform"]
`,
		"/acme/app/.gitslice/owners.yaml": `
version: 1
owners:
  - paths: ["api/**"]
    owners: ["@acme/api", "alice"]
    require: 2
`,
	}
	read := func(p string) ([]byte, bool, error) {
		d, ok := files[p]
		return []byte(d), ok, nil
	}

	// Nearest file exists and has no rule for main.go: no owner rule applies.
	if r, ok, err := Lookup("/acme/app/main.go", read); err != nil || ok {
		t.Fatalf("Lookup(app/main.go) = %+v %v %v, want no match", r, ok, err)
	}

	// Nearest file matches api/** with rule index 0.
	m, ok, err := LookupMatch("/acme/app/api/handler.go", read)
	if err != nil || !ok {
		t.Fatalf("LookupMatch(app/api/handler.go) = %+v %v %v", m, ok, err)
	}
	if m.ConfigPath != "/acme/app/.gitslice/owners.yaml" || m.RuleIndex != 0 || m.Rule.Require != 2 {
		t.Fatalf("LookupMatch = %+v", m)
	}

	// Sibling folder without its own owners.yaml falls back to account root.
	r, ok, err := Lookup("/acme/lib/util.go", read)
	if err != nil || !ok || !reflect.DeepEqual(r.Owners, []string{"@acme/platform"}) {
		t.Fatalf("Lookup(lib/util.go) = %+v %v %v", r, ok, err)
	}

	// Unmatched path under sibling folder has no owner.
	if r, ok, err := Lookup("/acme/lib/README.md", read); err != nil || ok {
		t.Fatalf("Lookup(lib/README.md) = %+v %v %v, want no match", r, ok, err)
	}
}

func TestLookupErrors(t *testing.T) {
	boom := errors.New("read failed")
	if _, _, err := Lookup("/acme/app/main.go", func(string) ([]byte, bool, error) {
		return nil, false, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Lookup read error = %v, want %v", err, boom)
	}

	if _, _, err := Lookup("/acme/app/main.go", func(cfg string) ([]byte, bool, error) {
		if cfg == "/acme/.gitslice/owners.yaml" {
			return []byte("version: 2\n"), true, nil
		}
		return nil, false, nil
	}); err == nil {
		t.Fatal("expected parse error from malformed owners.yaml")
	}
}

func TestParseGroupRef(t *testing.T) {
	cases := []struct {
		in      string
		account string
		name    string
		ok      bool
	}{
		{in: "@acme/api-agents", account: "acme", name: "api-agents", ok: true},
		{in: " @Acme/Core ", account: "acme", name: "core", ok: true},
		{in: "alice", ok: false},
		{in: "@alice", ok: false},
		{in: "@acme/", ok: false},
		{in: "@/core", ok: false},
		{in: "@acme/a/b", ok: false},
		{in: "", ok: false},
	}
	for _, tc := range cases {
		acc, name, ok := ParseGroupRef(tc.in)
		if ok != tc.ok || acc != tc.account || name != tc.name {
			t.Errorf("ParseGroupRef(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.in, acc, name, ok, tc.account, tc.name, tc.ok)
		}
	}
}

func TestParseRequireHuman(t *testing.T) {
	f, err := Parse("/acme/payments", []byte(`
version: 1
owners:
  - paths: ["billing/**"]
    owners: ["@acme/billing", "alice"]
    require: 1
    require_human: 1
  - paths: ["deploy/**"]
    owners: ["alice"]
    require: 0
    require_human: 1
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(f.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(f.Rules))
	}
	if f.Rules[0].Require != 1 || f.Rules[0].RequireHuman != 1 {
		t.Fatalf("rule[0] = %+v, want Require=1 RequireHuman=1", f.Rules[0])
	}
	if f.Rules[1].Require != 0 || f.Rules[1].RequireHuman != 1 {
		t.Fatalf("rule[1] = %+v, want Require=0 RequireHuman=1", f.Rules[1])
	}

	if _, err := Parse("/acme/payments", []byte(`
version: 1
owners:
  - paths: ["billing/**"]
    owners: ["alice"]
    require_human: -1
`)); err == nil {
		t.Fatal("expected error for negative require_human")
	}
}
