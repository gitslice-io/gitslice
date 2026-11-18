package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func tarGz(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sumLine(name string, data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

func TestReleaseAssetsAndArchives(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "gs_linux_amd64.tar.gz"},
		{"darwin", "arm64", "gs_darwin_arm64.tar.gz"},
		{"windows", "amd64", "gs_windows_amd64.zip"},
	} {
		if got, err := ReleaseAsset(tc.goos, tc.goarch); err != nil || got != tc.want {
			t.Fatalf("ReleaseAsset(%s, %s) = %q, %v", tc.goos, tc.goarch, got, err)
		}
	}
	if _, err := ReleaseAsset("plan9", "amd64"); err == nil {
		t.Fatal("plan9 has no release")
	}

	archive := tarGz(t, "gs", []byte("binary"))
	if got, err := ExtractGS("gs_linux_amd64.tar.gz", archive); err != nil || string(got) != "binary" {
		t.Fatalf("extract tar.gz = %q, %v", got, err)
	}
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("gs.exe")
	_, _ = w.Write([]byte("exe"))
	_ = zw.Close()
	if got, err := ExtractGS("gs_windows_amd64.zip", zbuf.Bytes()); err != nil || string(got) != "exe" {
		t.Fatalf("extract zip = %q, %v", got, err)
	}
	if _, err := ExtractGS("gs_linux_amd64.tar.gz", tarGz(t, "README", []byte("x"))); err == nil {
		t.Fatal("an archive without gs must be refused")
	}

	sums := []byte(sumLine("other.tar.gz", []byte("o")) + sumLine("gs_linux_amd64.tar.gz", archive))
	if err := VerifyChecksum(sums, "gs_linux_amd64.tar.gz", archive); err != nil {
		t.Fatal(err)
	}
	if err := VerifyChecksum(sums, "gs_linux_amd64.tar.gz", []byte("tampered")); err == nil {
		t.Fatal("a tampered download must be refused")
	}
	if err := VerifyChecksum(sums, "gs_linux_arm64.tar.gz", archive); err == nil {
		t.Fatal("an asset without a checksum must be refused")
	}
}

func TestVersionComparison(t *testing.T) {
	if !SameVersion("v0.4.0", "0.4.0") || SameVersion("v0.4.0", "v0.4.1") || SameVersion("", "") {
		t.Fatal("SameVersion")
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v0.5.0", "v0.4.9", true},
		{"v0.4.10", "v0.4.9", true},
		{"v0.4.0", "v0.4.0", false},
		{"v0.3.0", "v0.4.0", false},
		{"dev", "v0.4.0", false},
		{"v0.4.0", "dev", true},
		{"v1.0.0-rc1", "v0.9.0", true},
	} {
		if got := NewerVersion(tc.a, tc.b); got != tc.want {
			t.Fatalf("NewerVersion(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

func TestExtractGSRejectsOversizedArchiveEntryWithoutTruncation(t *testing.T) {
	origMax := maxReleaseAssetBytes
	maxReleaseAssetBytes = 1024
	defer func() { maxReleaseAssetBytes = origMax }()

	oversized := bytes.Repeat([]byte("A"), 1025)
	if _, err := ExtractGS("gs_linux_amd64.tar.gz", tarGz(t, "gs", oversized)); err == nil {
		t.Fatal("expected tar.gz entry exceeding maxReleaseAssetBytes to fail instead of truncating")
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, err := zw.Create("gs.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(oversized); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractGS("gs_windows_amd64.zip", zbuf.Bytes()); err == nil {
		t.Fatal("expected zip entry exceeding maxReleaseAssetBytes to fail instead of truncating")
	}
}

func TestReleaseHTTPClientRejectsHTTPSDowngradeRedirect(t *testing.T) {
	httpTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer httpTarget.Close()

	httpsOrigin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, httpTarget.URL+"/v1.2.3", http.StatusFound)
	}))
	defer httpsOrigin.Close()

	originURL, err := url.Parse(httpsOrigin.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := httpsOrigin.Client()
	client.CheckRedirect = NewReleaseHTTPClient(5*time.Second, originURL).CheckRedirect

	resp, err := client.Get(httpsOrigin.URL + "/latest")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("expected HTTPS -> HTTP redirect downgrade error, got: %v", err)
	}
}
