package cli_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestGitFetchWithoutSideBand fetches a slice the way minimal Git clients do,
// such as the importer behind Cloudflare Artifacts: it wants the head with
// ofs-delta but asks for neither side-band nor no-progress. Git then writes
// pack progress to stderr, and that output must not end up inside the
// packfile the client reads.
func TestGitFetchWithoutSideBand(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	submitWorkspaceFile(t, home, workspace, "sideband.go", "package payment\nconst SideBand = 1\n", "side-band target")

	base := "http://" + ts.gitAddr + "/git/acme/payment.git"
	do := func(method, url, contentType string, body []byte) []byte {
		t.Helper()
		req, err := http.NewRequest(method, url, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: %d\n%s", method, url, res.StatusCode, data)
		}
		return data
	}

	refs := do(http.MethodGet, base+"/info/refs?service=git-upload-pack", "", nil)
	head := ""
	scanner := bufio.NewScanner(bytes.NewReader(refs))
	for scanner.Scan() {
		if i := strings.Index(scanner.Text(), " refs/heads/main"); i >= 40 {
			head = scanner.Text()[i-40 : i]
			break
		}
	}
	if head == "" {
		t.Fatalf("no refs/heads/main in advertisement:\n%q", refs)
	}

	want := fmt.Sprintf("want %s ofs-delta\n", head)
	body := []byte(fmt.Sprintf("%04x%s0000%04x%s", len(want)+4, want, len("done\n")+4, "done\n"))
	pack := do(http.MethodPost, base+"/git-upload-pack", "application/x-git-upload-pack-request", body)
	if !bytes.HasPrefix(pack, []byte("0008NAK\nPACK")) {
		end := len(pack)
		if end > 200 {
			end = 200
		}
		t.Fatalf("upload-pack without side-band should answer NAK then the packfile, got:\n%q", pack[:end])
	}
}
