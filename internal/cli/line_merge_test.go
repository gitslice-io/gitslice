package cli

import (
	"testing"
)

func TestMergeTextLines(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		local  string
		remote string
		want   string
		ok     bool
	}{
		{
			name:   "non overlapping replacements",
			base:   "a\nb\nc\n",
			local:  "a\nlocal b\nc\n",
			remote: "remote a\nb\nc\n",
			want:   "remote a\nlocal b\nc\n",
			ok:     true,
		},
		{
			name:   "adjacent replacements",
			base:   "a\nb\nc\n",
			local:  "a\nlocal b\nc\n",
			remote: "a\nb\nremote c\n",
			want:   "a\nlocal b\nremote c\n",
			ok:     true,
		},
		{
			name:   "overlapping replacements conflict",
			base:   "a\nb\nc\n",
			local:  "a\nlocal b\nc\n",
			remote: "a\nremote b\nc\n",
			ok:     false,
		},
		{
			name:   "same insertion point conflict",
			base:   "a\nb\n",
			local:  "a\nlocal\nb\n",
			remote: "a\nremote\nb\n",
			ok:     false,
		},
		{
			name:   "identical side edit",
			base:   "a\nb\n",
			local:  "a\nshared\nb\n",
			remote: "a\nshared\nb\n",
			want:   "a\nshared\nb\n",
			ok:     true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mergeTextLines([]byte(tc.base), []byte(tc.local), []byte(tc.remote))
			if ok != tc.ok {
				t.Fatalf("merge ok = %v, want %v", ok, tc.ok)
			}
			if ok && string(got) != tc.want {
				t.Fatalf("merged text mismatch:\nwant:\n%s\ngot:\n%s", tc.want, string(got))
			}
		})
	}
}
