package gitimport

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestValidateSource(t *testing.T) {
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
		{name: "dash option prefix", source: "-u/repo", wantErr: true},
		{name: "empty", source: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, local, err := ValidateSource(tt.source)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateSource(%q) returned %q, want error", tt.source, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateSource(%q) returned error: %v", tt.source, err)
			}
			if got != tt.want {
				t.Fatalf("ValidateSource(%q) = %q, want %q", tt.source, got, tt.want)
			}
			if local {
				t.Fatalf("ValidateSource(%q) reported local for a remote source", tt.source)
			}
		})
	}
}

func TestValidateSourceAllowsConfiguredHost(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOWED_HOSTS", " Git.Example.COM ")
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "")
	prevResolve := ResolvePublicHost
	ResolvePublicHost = func(_ context.Context, host string, allowPrivate bool) (netip.Addr, error) {
		if host == "git.example.com" && !allowPrivate {
			return netip.MustParseAddr("93.184.216.34"), nil
		}
		return netip.Addr{}, errors.New("unexpected host")
	}
	t.Cleanup(func() { ResolvePublicHost = prevResolve })

	const source = "https://git.example.com/owner/repo.git"
	got, local, err := ValidateSource(source)
	if err != nil {
		t.Fatalf("ValidateSource(%q) returned error: %v", source, err)
	}
	if got != source {
		t.Fatalf("ValidateSource(%q) = %q, want %q", source, got, source)
	}
	if local {
		t.Fatalf("ValidateSource(%q) reported local for a remote source", source)
	}
}

func TestValidateSourceRejectsNonPublicConfiguredHosts(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOWED_HOSTS", "127.0.0.1,localhost,169.254.169.254,[::1],internal.corp")
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "")
	prevResolve := ResolvePublicHost
	ResolvePublicHost = func(ctx context.Context, host string, allowPrivate bool) (netip.Addr, error) {
		if host == "internal.corp" {
			return netip.Addr{}, errors.New("webhook target resolves to non-public address 10.0.0.5")
		}
		return prevResolve(ctx, host, allowPrivate)
	}
	t.Cleanup(func() { ResolvePublicHost = prevResolve })

	for _, source := range []string{
		"https://127.0.0.1/o/r.git",
		"https://localhost/o/r.git",
		"https://169.254.169.254/o/r.git",
		"https://[::1]/o/r.git",
		"https://internal.corp/o/r.git",
	} {
		if _, _, err := ValidateSource(source); err == nil {
			t.Fatalf("ValidateSource(%q) succeeded, want rejection for non-public host", source)
		}
	}
}

func TestValidateSourceLocalPathsGatedByFlag(t *testing.T) {
	// Disabled by default: local paths are rejected.
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "")
	for _, source := range []string{"/srv/repo", "file:///srv/repo"} {
		if _, _, err := ValidateSource(source); err == nil {
			t.Fatalf("ValidateSource(%q) succeeded, want error while local import disabled", source)
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
		got, local, err := ValidateSource(tc.source)
		if err != nil {
			t.Fatalf("ValidateSource(%q) returned error: %v", tc.source, err)
		}
		if got != tc.want {
			t.Fatalf("ValidateSource(%q) = %q, want %q", tc.source, got, tc.want)
		}
		if !local {
			t.Fatalf("ValidateSource(%q) did not report local", tc.source)
		}
	}

	// Even with local enabled, ssh/ext sources stay rejected.
	for _, source := range []string{"git@github.com:o/r.git", "ext::sh -c id"} {
		if _, _, err := ValidateSource(source); err == nil {
			t.Fatalf("ValidateSource(%q) succeeded, want error", source)
		}
	}
}
