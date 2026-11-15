package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckoutListingCache covers fb_3fc4 (5): a workspace made at a commit
// whose listing this machine already has takes the listing from the local
// cache (the server still answers the header, so access is checked), and
// GS_LISTING_CACHE=off goes to the server.
func TestCheckoutListingCache(t *testing.T) {
	isolateClientCache(t)
	ts := startTestServer(t)
	home := t.TempDir()
	cacheDir := filepath.Join(home, ".cache", "gitslice")
	first := t.TempDir()
	loginTestCLI(t, ts, home, first)
	runCLI(t, home, first, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, first, "acme/payment/keep.txt", "keep\n")
	writeWorkspaceFile(t, first, "acme/payment/drop.txt", "drop\n")
	runCLI(t, home, first, "create", "--all", "--message", "two files", "--submit")

	// With a warm object cache, a checkout asks for the listing only, and
	// keeps it.
	runCLI(t, home, t.TempDir(), "init", "acme/payment")
	listings, _ := filepath.Glob(filepath.Join(cacheDir, "listings", "*", "*.json"))
	if len(listings) == 0 {
		t.Fatalf("no listing cached under %s", cacheDir)
	}
	// Take drop.txt out of every cached listing: a checkout that uses the
	// cache does not have it.
	for _, path := range listings {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		i := strings.Index(text, "drop.txt")
		if i < 0 {
			continue
		}
		start := strings.LastIndex(text[:i], "{")
		end := i + strings.Index(text[i:], "}") + 1
		text = text[:start] + text[end:]
		text = strings.ReplaceAll(strings.ReplaceAll(text, ",,", ","), "[,", "[")
		text = strings.ReplaceAll(text, ",]", "]")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cached := t.TempDir()
	runCLI(t, home, cached, "init", "acme/payment")
	if _, err := os.Stat(filepath.Join(cached, "acme/payment/drop.txt")); err == nil {
		t.Fatal("the checkout did not use the cached listing")
	}
	assertWorkspaceFile(t, cached, "acme/payment/keep.txt", "keep\n")

	t.Setenv("GS_LISTING_CACHE", "off")
	fresh := t.TempDir()
	runCLI(t, home, fresh, "init", "acme/payment")
	assertWorkspaceFile(t, fresh, "acme/payment/drop.txt", "drop\n")
}
