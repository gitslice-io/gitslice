package service

import "testing"

func TestValidateImportSource(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOWED_HOSTS", "")

	tests := []struct {
		name    string
		source  string
		want    string
		wantErr bool
	}{
		{
			name:   "owner repo shorthand",
			source: "owner/repo",
			want:   "https://github.com/owner/repo.git",
		},
		{
			name:   "owner repo shorthand strips git suffix",
			source: "owner/repo.git",
			want:   "https://github.com/owner/repo.git",
		},
		{
			name:   "full GitHub URL",
			source: "https://github.com/owner/repo.git",
			want:   "https://github.com/owner/repo.git",
		},
		{name: "file URL", source: "file:///etc", wantErr: true},
		{name: "absolute path", source: "/etc/passwd", wantErr: true},
		{name: "relative path", source: "../repo", wantErr: true},
		{name: "scp style SSH", source: "git@github.com:o/r.git", wantErr: true},
		{name: "ext transport", source: "ext::sh -c id", wantErr: true},
		{name: "HTTP URL", source: "http://github.com/o/r", wantErr: true},
		{name: "unlisted host", source: "https://evil.com/o/r", wantErr: true},
		{name: "URL user info", source: "https://user@github.com/o/r", wantErr: true},
		{name: "non-HTTPS port", source: "https://github.com:8443/o/r", wantErr: true},
		{name: "empty", source: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateImportSource(tt.source)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateImportSource(%q) returned %q, want error", tt.source, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateImportSource(%q) returned error: %v", tt.source, err)
			}
			if got != tt.want {
				t.Fatalf("validateImportSource(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

func TestValidateImportSourceAllowsConfiguredHost(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOWED_HOSTS", " Git.Example.COM ")

	const source = "https://git.example.com/owner/repo.git"
	got, err := validateImportSource(source)
	if err != nil {
		t.Fatalf("validateImportSource(%q) returned error: %v", source, err)
	}
	if got != source {
		t.Fatalf("validateImportSource(%q) = %q, want %q", source, got, source)
	}
}
