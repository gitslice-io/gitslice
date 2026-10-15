package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestLegacyRepoCommandIsRemoved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"repo", "import", "github", "owner/repo"})
	if err == nil {
		t.Fatalf("legacy repo command unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), `unknown command "repo"`) && !strings.Contains(stderr.String(), `unknown command "repo"`) {
		t.Fatalf("legacy repo command error = %v\nstderr:\n%s", err, stderr.String())
	}
}

func TestBrowsePrintsWebURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "root",
			args: []string{"browse", "--web-url", "127.0.0.1:8082", "--print"},
			want: "http://127.0.0.1:8082/",
		},
		{
			name: "route",
			args: []string{"browse", "claims", "--web-url", "http://127.0.0.1:8082", "--print"},
			want: "http://127.0.0.1:8082/claims",
		},
		{
			name: "base path and query",
			args: []string{"browse", "slices?account=nic", "--web-url", "https://web.example.invalid/ui/", "--print"},
			want: "https://web.example.invalid/ui/slices?account=nic",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			// Dir keeps browse from finding a workspace around the test's own
			// working directory, as when the source is checked out with gs init.
			r := Runner{Home: t.TempDir(), Dir: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
			if err := r.Run(context.Background(), tc.args); err != nil {
				t.Fatalf("browse failed: %v\nstderr:\n%s", err, stderr.String())
			}
			if strings.TrimSpace(stdout.String()) != tc.want {
				t.Fatalf("browse URL = %q, want %q", strings.TrimSpace(stdout.String()), tc.want)
			}
		})
	}
}

func readSignupApprovalURL(t *testing.T, r io.Reader) string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("approval URL was not printed")
	return ""
}
