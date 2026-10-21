package storage

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeChangesetText(t *testing.T) {
	long := strings.Repeat("word ", 60) // 300 runes
	cases := []struct {
		name, title, description, wantTitle, wantDescription string
	}{
		{"plain", "Add login", "", "Add login", ""},
		{"trims and keeps a description", "  Add login \n", " Uses OAuth. ", "Add login", "Uses OAuth."},
		{"a message is a subject and a body", "Add login\n\nUses OAuth.\nSee the design.", "", "Add login", "Uses OAuth.\nSee the design."},
		{"windows line endings", "Add login\r\n\r\nUses OAuth.", "", "Add login", "Uses OAuth."},
		{"the body comes before a given description", "Add login\n\nBody", "Extra", "Add login", "Body\n\nExtra"},
		{"no title", "", "Just a description", "", "Just a description"},
		{"blank first line is not a subject", "\n\nAdd login\n\nBody", "", "Add login", "Body"},
	}
	for _, tc := range cases {
		title, description := NormalizeChangesetText(tc.title, tc.description)
		if title != tc.wantTitle || description != tc.wantDescription {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, title, description, tc.wantTitle, tc.wantDescription)
		}
	}

	title, description := NormalizeChangesetText(long, "")
	if n := utf8.RuneCountInString(title); n > MaxChangesetTitleRunes || !strings.HasSuffix(title, "…") {
		t.Fatalf("long title = %q (%d runes), want at most %d ending in an ellipsis", title, n, MaxChangesetTitleRunes)
	}
	if description != strings.TrimSpace(long) {
		t.Fatalf("the full first line should start the description, got %q", description)
	}
	// Nothing is lost: what a client sent is still in the title or description.
	if !strings.Contains(description, "word word word") {
		t.Fatal("description lost the text")
	}
}

func TestNormalizeChangesetTextIsIdempotent(t *testing.T) {
	title, description := NormalizeChangesetText("Add login\n\nBody", "Extra")
	again, againDescription := NormalizeChangesetText(title, description)
	if again != title || againDescription != description {
		t.Fatalf("a second pass changed (%q, %q) to (%q, %q)", title, description, again, againDescription)
	}
}

func TestCommitMessage(t *testing.T) {
	if got := CommitMessage("Add login", "Uses OAuth."); got != "Add login\n\nUses OAuth." {
		t.Fatalf("CommitMessage = %q", got)
	}
	if got := CommitMessage("Add login", ""); got != "Add login" {
		t.Fatalf("CommitMessage without a description = %q", got)
	}
	if got := CommitMessage(" ", "Only"); got != "Only" {
		t.Fatalf("CommitMessage without a title = %q", got)
	}
}
