package r2

import "testing"

func TestResolveObjectKey(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		key     string
		want    string
		wantErr bool
	}{
		{
			name: "plain key",
			key:  "blobs/sha256/ab/cd/hash",
			want: "blobs/sha256/ab/cd/hash",
		},
		{
			name:   "prefix",
			prefix: "repo-objects",
			key:    "blobs/hash",
			want:   "repo-objects/blobs/hash",
		},
		{
			name:   "prefix with slashes",
			prefix: "/repo-objects/",
			key:    "blobs/hash",
			want:   "repo-objects/blobs/hash",
		},
		{
			name:   "nested prefix is cleaned",
			prefix: "repo//objects/",
			key:    "trees//sha256/hash.json",
			want:   "repo/objects/trees/sha256/hash.json",
		},
		{
			name:   "single leading slash matches filesystem store",
			prefix: "repo",
			key:    "/blobs/hash",
			want:   "repo/blobs/hash",
		},
		{
			name:    "empty key rejected",
			key:     "",
			wantErr: true,
		},
		{
			name:    "dot key rejected",
			key:     ".",
			wantErr: true,
		},
		{
			name:    "parent key rejected",
			key:     "..",
			wantErr: true,
		},
		{
			name:    "parent-prefixed key rejected",
			key:     "../blob",
			wantErr: true,
		},
		{
			name:    "parent after clean rejected",
			key:     "blob/../../other",
			wantErr: true,
		},
		{
			name:    "absolute key rejected after one leading slash is stripped",
			key:     "//blobs/hash",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveObjectKey(tt.prefix, tt.key)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveObjectKey(%q, %q) error = nil, want error", tt.prefix, tt.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveObjectKey(%q, %q) error = %v", tt.prefix, tt.key, err)
			}
			if got != tt.want {
				t.Fatalf("resolveObjectKey(%q, %q) = %q, want %q", tt.prefix, tt.key, got, tt.want)
			}
		})
	}
}

func TestRangeHeader(t *testing.T) {
	tests := []struct {
		name    string
		offset  int64
		length  int64
		want    string
		wantErr bool
	}{
		{name: "full object", want: ""},
		{name: "offset only", offset: 5, want: "bytes=5-"},
		{name: "length only", length: 10, want: "bytes=0-9"},
		{name: "offset and length", offset: 5, length: 10, want: "bytes=5-14"},
		{name: "negative offset treated as zero", offset: -5, length: 10, want: "bytes=0-9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rangeHeader(tt.offset, tt.length)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("rangeHeader(%d, %d) error = nil, want error", tt.offset, tt.length)
				}
				return
			}
			if err != nil {
				t.Fatalf("rangeHeader(%d, %d) error = %v", tt.offset, tt.length, err)
			}
			if got != tt.want {
				t.Fatalf("rangeHeader(%d, %d) = %q, want %q", tt.offset, tt.length, got, tt.want)
			}
		})
	}
}
