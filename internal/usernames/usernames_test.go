package usernames

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "canonical", input: "taylor-name", want: "taylor-name"},
		{name: "normalizes case whitespace and underscore", input: " Taylor_Name ", want: "taylor-name"},
		{name: "minimum length", input: "abc", wantErr: "at least 4 characters"},
		{name: "reserved brand", input: "OpenAI", wantErr: "username is reserved"},
		{name: "reserved public figure", input: "Taylor_Swift", wantErr: "username is reserved"},
		{name: "leading hyphen", input: "-valid", wantErr: "must not start or end"},
		{name: "invalid character", input: "valid.name", wantErr: "may contain only"},
		{name: "maximum length", input: strings.Repeat("a", 64), wantErr: "63 characters or fewer"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Normalize(test.input)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Normalize(%q) error = %v, want containing %q", test.input, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("Normalize(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestReservedNamesAreCanonicalAndUnique(t *testing.T) {
	names := ReservedNames()
	if len(names) < 100 {
		t.Fatalf("reserved names = %d, want a maintained broad list", len(names))
	}
	for i, name := range names {
		if i > 0 && names[i-1] >= name {
			t.Fatalf("reserved names are not strictly sorted at %q", name)
		}
		if len(name) < MinLength || len(name) > MaxLength || strings.ToLower(name) != name {
			t.Fatalf("reserved username %q is not canonical", name)
		}
		for _, r := range name {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			t.Fatalf("reserved username %q contains invalid character %q", name, r)
		}
	}
}
