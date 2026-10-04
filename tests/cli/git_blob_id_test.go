package cli_test

import (
	"database/sql"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUploadedFilesGetTheirGitBlobID checks that the id recorded when the CLI
// uploads a file is the one Git itself computes for the same bytes.
func TestUploadedFilesGetTheirGitBlobID(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	content := "package payment\n\nconst GitID = \"recorded at upload\"\n"
	submitWorkspaceFile(t, home, workspace, "gitid.go", content, "git id")

	hash := exec.Command("git", "hash-object", "--stdin")
	hash.Stdin = strings.NewReader(content)
	out, err := hash.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(out))

	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got sql.NullString
	if err := db.QueryRow(`select git_blob_id from blobs where size = $1`, len(content)).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != want {
		t.Fatalf("git_blob_id = %v, want %s (git hash-object)", got, want)
	}

	// The projection stores the same file under the same id, so a Git tree for
	// the slice can be built from these ids without reading the files.
	clone := filepath.Join(t.TempDir(), "payment")
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", "http://"+ts.gitAddr+"/git/acme/payment.git", clone)
	if projected := strings.TrimSpace(runGit(t, "", "-C", clone, "rev-parse", "HEAD:acme/payment/gitid.go")); projected != got.String {
		t.Fatalf("the projected file has id %s, the recorded id is %s", projected, got.String)
	}
}
