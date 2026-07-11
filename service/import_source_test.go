package service

import "testing"

func TestValidateImportSource(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOWED_HOSTS", "")
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "")

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
			got, local, err := validateImportSource(tt.source)
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
			if local {
				t.Fatalf("validateImportSource(%q) reported local for a remote source", tt.source)
			}
		})
	}
}

func TestValidateImportSourceAllowsConfiguredHost(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOWED_HOSTS", " Git.Example.COM ")
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "")

	const source = "https://git.example.com/owner/repo.git"
	got, local, err := validateImportSource(source)
	if err != nil {
		t.Fatalf("validateImportSource(%q) returned error: %v", source, err)
	}
	if got != source {
		t.Fatalf("validateImportSource(%q) = %q, want %q", source, got, source)
	}
	if local {
		t.Fatalf("validateImportSource(%q) reported local for a remote source", source)
	}
}

func TestValidateImportSourceLocalPathsGatedByFlag(t *testing.T) {
	// Disabled by default: local paths are rejected.
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "")
	for _, source := range []string{"/srv/repo", "file:///srv/repo"} {
		if _, _, err := validateImportSource(source); err == nil {
			t.Fatalf("validateImportSource(%q) succeeded, want error while local import disabled", source)
		}
	}

	// Enabled: local paths are accepted, normalized, and flagged local.
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	cases := []struct {
		source string
		want   string
	}{
		{source: "/srv/repo", want: "/srv/repo"},
		{source: "/srv/../srv/repo", want: "/srv/repo"},
		{source: "file:///srv/repo", want: "/srv/repo"},
	}
	for _, tc := range cases {
		got, local, err := validateImportSource(tc.source)
		if err != nil {
			t.Fatalf("validateImportSource(%q) returned error: %v", tc.source, err)
		}
		if got != tc.want {
			t.Fatalf("validateImportSource(%q) = %q, want %q", tc.source, got, tc.want)
		}
		if !local {
			t.Fatalf("validateImportSource(%q) did not report local", tc.source)
		}
	}

	// Even with local enabled, ssh/ext sources stay rejected.
	for _, source := range []string{"git@github.com:o/r.git", "ext::sh -c id"} {
		if _, _, err := validateImportSource(source); err == nil {
			t.Fatalf("validateImportSource(%q) succeeded, want error", source)
		}
	}
}
