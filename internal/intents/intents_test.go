package intents

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNormalizePattern(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "exact file", in: "/acme/app/src/main.go", want: "/acme/app/src/main.go"},
		{name: "trailing slash normalized", in: "/acme/app/src/billing/", want: "/acme/app/src/billing/**"},
		{name: "trailing glob slash", in: "/acme/app/src/**/", want: "/acme/app/src/**"},
		{name: "double slashes collapsed", in: "/acme//app///src/*.go", want: "/acme/app/src/*.go"},
		{name: "brace glob", in: "/acme/app/{billing,tax}/**/*.go", want: "/acme/app/{billing,tax}/**/*.go"},
		{name: "whitespace trimmed", in: "  /acme/app/foo.go  ", want: "/acme/app/foo.go"},
		{name: "empty rejected", in: "   ", wantErr: true},
		{name: "root rejected", in: "/", wantErr: true},
		{name: "missing leading slash", in: "acme/app/foo.go", wantErr: true},
		{name: "negation rejected", in: "!/acme/app/foo.go", wantErr: true},
		{name: "backslash rejected", in: `/acme/app\foo.go`, wantErr: true},
		{name: "dotdot traversal rejected", in: "/acme/app/../secret", wantErr: true},
		{name: "dot segment rejected", in: "/acme/app/./foo.go", wantErr: true},
		{name: "unclosed bracket rejected", in: "/acme/app/[abc", wantErr: true},
		{name: "control char rejected", in: "/acme/app/foo\nbar", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePattern(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizePattern(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizePattern(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizePattern(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateSlicePattern(t *testing.T) {
	included := []string{"/acme/payment", "/acme/shared/types"}

	valid := []struct {
		raw  string
		want string
	}{
		{"/acme/payment/billing/**", "/acme/payment/billing/**"},
		{"/acme/payment/", "/acme/payment/**"},
		{"/acme/payment/*.go", "/acme/payment/*.go"},
		{"/acme/shared/types/money.go", "/acme/shared/types/money.go"},
	}
	for _, tc := range valid {
		got, err := ValidateSlicePattern(tc.raw, included)
		if err != nil {
			t.Fatalf("ValidateSlicePattern(%q) unexpected error: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ValidateSlicePattern(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	invalid := []string{
		"/acme/**",
		"/**",
		"/acme/billing/**",
		"/acme/shared/**",
		"/acme/pay*/foo",
		"/acme/payment/../billing/**",
		"!/acme/payment/foo.go",
	}
	for _, raw := range invalid {
		if got, err := ValidateSlicePattern(raw, included); err == nil {
			t.Fatalf("ValidateSlicePattern(%q) = %q, expected out-of-slice error", raw, got)
		}
	}
}

func TestParseDurationAndFormatRemaining(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "30m", want: 30 * time.Minute},
		{in: "2h", want: 2 * time.Hour},
		{in: "1h30m", want: 90 * time.Minute},
		{in: "1d", want: 24 * time.Hour},
		{in: "1d12h", want: 36 * time.Hour},
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "8d", wantErr: true},
		{in: "169h", wantErr: true},
		{in: "0s", wantErr: true},
		{in: "-1h", wantErr: true},
		{in: "0d", wantErr: true},
		{in: "", wantErr: true},
		{in: "bogus", wantErr: true},
	}
	for _, tc := range tests {
		got, err := ParseDuration(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseDuration(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseDuration(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	remTests := []struct {
		d    time.Duration
		want string
	}{
		{0, "expired"},
		{-time.Second, "expired"},
		{45 * time.Second, "45s"},
		{25 * time.Minute, "25m"},
		{2 * time.Hour, "2h"},
		{105 * time.Minute, "1h45m"},
		{48 * time.Hour, "2d"},
		{27 * time.Hour, "1d3h"},
	}
	for _, tc := range remTests {
		if got := FormatRemaining(tc.d); got != tc.want {
			t.Fatalf("FormatRemaining(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestMatchesPathAndOverlappingPaths(t *testing.T) {
	candidates := []string{
		"/acme/app/src/billing/tax.go",
		"/acme/app/src/billing/sub/rate.go",
		"/acme/app/src/billing/README.md",
		"/acme/app/src/billing_extra/tax.go",
		"/acme/app/src/auth/login.go",
	}

	// Directory pattern without glob matches children via paths.Contains, never siblings.
	got := OverlappingPaths("/acme/app/src/billing", candidates)
	want := []string{
		"/acme/app/src/billing/README.md",
		"/acme/app/src/billing/sub/rate.go",
		"/acme/app/src/billing/tax.go",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("dir overlap = %v, want %v", got, want)
	}

	// Recursive glob after trailing slash normalization.
	norm, err := NormalizePattern("/acme/app/src/billing/")
	if err != nil {
		t.Fatalf("NormalizePattern error: %v", err)
	}
	got = OverlappingPaths(norm, candidates)
	if !slices.Equal(got, want) {
		t.Fatalf("trailing slash glob overlap = %v, want %v", got, want)
	}

	// Single-directory wildcard *.go must not match sub/rate.go or README.md.
	got = OverlappingPaths("/acme/app/src/billing/*.go", candidates)
	if !slices.Equal(got, []string{"/acme/app/src/billing/tax.go"}) {
		t.Fatalf("single-dir *.go overlap = %v", got)
	}
}

func TestPatternsOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/acme/app/src/billing", "/acme/app/src/billing/tax.go", true},
		{"/acme/app/src/billing", "/acme/app/src/billing_extra", false},
		{"/acme/app/src/**", "/acme/app/src/billing/*.go", true},
		{"/acme/app/**/*.go", "/acme/app/src/billing/**", true},
		{"/acme/app/**/*.go", "/acme/app/src/**/*.ts", false},
		{"/acme/app/src/*.go", "/acme/app/src/main.go", true},
		{"/acme/app/src/*.go", "/acme/app/src/sub/main.go", false},
		{"/acme/app/{billing,tax}/*.go", "/acme/app/tax/calc.go", true},
		{"/acme/app/{billing,tax}/*.go", "/acme/app/auth/calc.go", false},
		{"/acme/app/[ab]*.go", "/acme/app/billing.go", true},
		{"/acme/app/[ab]*.go", "/acme/app/zeta.go", false},
	}

	for _, tc := range tests {
		if got := PatternsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("PatternsOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := PatternsOverlap(tc.b, tc.a); got != tc.want {
			t.Errorf("PatternsOverlap(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestFindOverlapsAndFormatting(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	active := []ActiveIntent{
		{
			ID:               "int_1",
			SliceID:          "/acme/app",
			Pattern:          "/acme/app/src/billing/**",
			Message:          "migrating invoice tax",
			CreatorSubjectID: "subj_agent_a",
			CreatorName:      "agent-a",
			ExpiresAt:        now.Add(105 * time.Minute),
		},
		{
			ID:               "int_2",
			SliceID:          "/acme/app",
			Pattern:          "/acme/app/src/billing/**",
			Message:          "own intent",
			CreatorSubjectID: "subj_me",
			CreatorName:      "agent-me",
			ExpiresAt:        now.Add(2 * time.Hour),
		},
		{
			ID:               "int_expired",
			SliceID:          "/acme/app",
			Pattern:          "/acme/app/src/**",
			Message:          "stale",
			CreatorSubjectID: "subj_agent_b",
			CreatorName:      "agent-b",
			ExpiresAt:        now.Add(-time.Minute),
		},
	}
	openCS := []OpenChangeset{
		{
			ID:            "cs_100",
			Title:         "Fix tax rounding",
			Author:        "nic",
			AffectedPaths: []string{"/acme/app/src/billing/tax.go", "/acme/app/README.md"},
		},
		{
			ID:            "cs_self",
			Title:         "Current CS",
			Author:        "agent-me",
			AffectedPaths: []string{"/acme/app/src/billing/tax.go"},
		},
	}

	warnings := FindPathOverlaps(
		[]string{"/acme/app/src/billing/tax.go", "/acme/app/src/billing/calc.go"},
		active,
		openCS,
		OverlapOptions{ExcludeSubjectID: "subj_me", ExcludeChangesetID: "cs_self", Now: now},
	)
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want 2: %+v", len(warnings), warnings)
	}
	if warnings[0].Kind != "intent" || warnings[0].ID != "int_1" || len(warnings[0].MatchedPaths) != 2 {
		t.Fatalf("unexpected intent warning: %+v", warnings[0])
	}
	if warnings[1].Kind != "changeset" || warnings[1].ID != "cs_100" || !slices.Equal(warnings[1].MatchedPaths, []string{"/acme/app/src/billing/tax.go"}) {
		t.Fatalf("unexpected changeset warning: %+v", warnings[1])
	}

	line0 := FormatWarningLine(warnings[0], now)
	if !strings.Contains(line0, "int_1") || !strings.Contains(line0, "agent-a") || !strings.Contains(line0, "1h45m left") || !strings.Contains(line0, "migrating invoice tax") {
		t.Fatalf("unexpected intent warning line: %q", line0)
	}
	line1 := FormatWarningLine(warnings[1], now)
	if !strings.Contains(line1, "cs_100") || !strings.Contains(line1, "nic") || !strings.Contains(line1, "Fix tax rounding") {
		t.Fatalf("unexpected changeset warning line: %q", line1)
	}

	patWarnings, err := FindPatternOverlaps(
		"/acme/app/src/billing/*.go",
		active,
		openCS,
		OverlapOptions{ExcludeSubjectID: "subj_me", ExcludeChangesetID: "cs_self", Now: now},
	)
	if err != nil {
		t.Fatalf("FindPatternOverlaps error: %v", err)
	}
	if len(patWarnings) != 2 {
		t.Fatalf("got %d pattern warnings, want 2: %+v", len(patWarnings), patWarnings)
	}
}

func TestExtractMentionedIntentIDs(t *testing.T) {
	ids := ExtractMentionedIntentIDs(
		"Refactor billing (clears int_0123456789abcdef0123456789abcdef)",
		"Also closes int_42 and repeats int_0123456789abcdef0123456789abcdef; ignore my_int_9 and int_",
	)
	want := []string{"int_0123456789abcdef0123456789abcdef", "int_42"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ExtractMentionedIntentIDs = %v, want %v", ids, want)
	}
}
