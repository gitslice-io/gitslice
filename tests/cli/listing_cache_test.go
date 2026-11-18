package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stripCachedListingEntry(t *testing.T, listingPath, substr string) bool {
	t.Helper()
	data, err := os.ReadFile(listingPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal cached listing %s: %v", listingPath, err)
	}
	kept := make([]json.RawMessage, 0, len(doc.Entries))
	removed := false
	for _, entry := range doc.Entries {
		if strings.Contains(string(entry), substr) {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return false
	}
	doc.Entries = kept
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listingPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return true
}

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
		stripCachedListingEntry(t, path, "drop.txt")
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

// TestCheckoutListingDelta: once the slice has moved past the listing this
// machine has, a checkout asks for the changes since it (CheckoutHeader.delta)
// and applies them, so the new workspace has the new commit's files: added,
// changed and removed ones. The proof that the cached listing was the base: a
// file the test takes out of it, and which did not change since, is missing
// from the new workspace.
func TestCheckoutListingDelta(t *testing.T) {
	isolateClientCache(t)
	ts := startTestServer(t)
	home := t.TempDir()
	cacheDir := filepath.Join(home, ".cache", "gitslice")
	work := t.TempDir()
	loginTestCLI(t, ts, home, work)
	runCLI(t, home, work, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, work, "acme/payment/keep.txt", "keep\n")
	writeWorkspaceFile(t, work, "acme/payment/marker.txt", "marker\n")
	writeWorkspaceFile(t, work, "acme/payment/old/drop.txt", "drop\n")
	writeWorkspaceFile(t, work, "acme/payment/edit.txt", "before\n")
	runCLI(t, home, work, "create", "--all", "--message", "first", "--submit")

	runCLI(t, home, t.TempDir(), "init", "acme/payment") // caches the listing
	listings, _ := filepath.Glob(filepath.Join(cacheDir, "listings", "*", "*.json"))
	if len(listings) != 1 {
		t.Fatalf("cached listings = %v, want one", listings)
	}
	if !stripCachedListingEntry(t, listings[0], "marker.txt") {
		t.Fatalf("marker.txt is not in the cached listing %s", listings[0])
	}

	runCLI(t, home, work, "sync")
	writeWorkspaceFile(t, work, "acme/payment/edit.txt", "after\n")
	writeWorkspaceFile(t, work, "acme/payment/new/added.txt", "added\n")
	if err := os.RemoveAll(filepath.Join(work, "acme/payment/old")); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, work, "create", "--all", "--message", "second", "--submit")

	next := t.TempDir()
	runCLI(t, home, next, "init", "acme/payment")
	assertWorkspaceFile(t, next, "acme/payment/keep.txt", "keep\n")
	assertWorkspaceFile(t, next, "acme/payment/edit.txt", "after\n")
	assertWorkspaceFile(t, next, "acme/payment/new/added.txt", "added\n")
	if _, err := os.Stat(filepath.Join(next, "acme/payment/old")); err == nil {
		t.Fatal("old/ was removed upstream but is in the new workspace")
	}
	if _, err := os.Stat(filepath.Join(next, "acme/payment/marker.txt")); err == nil {
		t.Fatal("marker.txt is in the new workspace: the checkout did not start from the cached listing")
	}
}
