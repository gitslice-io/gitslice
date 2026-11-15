package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUpgradeRejectsInvalidReleaseTagsAndInsecureBaseURL(t *testing.T) {
	for _, invalid := range []string{
		"../evil",
		"v1.2.3/../../pwn",
		"v1.2",
		"1.2.3",
		"v1.2.3;rm -rf",
		"latest",
		"v1.2.3\x1b[31m",
	} {
		if err := validateReleaseTag(invalid); err == nil {
			t.Fatalf("validateReleaseTag(%q) unexpectedly succeeded", invalid)
		}
	}
	for _, valid := range []string{
		"v0.4.2",
		"v1.0.0-rc.1",
	} {
		if err := validateReleaseTag(valid); err != nil {
			t.Fatalf("validateReleaseTag(%q) err=%v", valid, err)
		}
	}

	// Remote HTTP base URL must be rejected; loopback HTTP is allowed for local tests.
	if _, err := validateReleaseBaseURL("http://releases.evil.example/gs"); err == nil {
		t.Fatal("expected plaintext non-loopback GS_DOWNLOAD_BASE to be rejected")
	}
	if _, err := validateReleaseBaseURL("http://127.0.0.1:8080/releases"); err != nil {
		t.Fatalf("expected loopback HTTP test base to be allowed, got: %v", err)
	}
	if _, err := validateReleaseBaseURL("https://gitslice.io/releases/"); err != nil {
		t.Fatalf("expected HTTPS release base to be allowed, got: %v", err)
	}

	// Poisoned/invalid cached tags in update-check.json are ignored.
	home := t.TempDir()
	var stderr bytes.Buffer
	r := Runner{Home: home, Stderr: &stderr}
	if err := r.writeUpdateCheckState(updateCheckState{
		CheckedAt: time.Now().UTC(),
		Latest:    "v9.9.9\x1b[31m-injected",
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.latestKnownRelease(context.Background()); got != "" {
		t.Fatalf("expected poisoned cached update tag to be rejected, got %q", got)
	}
	if info, err := os.Stat(r.updateCheckPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 permissions on update-check.json, got %v (err=%v)", info.Mode().Perm(), err)
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
	client.CheckRedirect = newReleaseHTTPClient(5*time.Second, originURL).CheckRedirect

	resp, err := client.Get(httpsOrigin.URL + "/latest")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("expected HTTPS -> HTTP redirect downgrade error, got: %v", err)
	}
}

func TestExtractGSRejectsOversizedArchiveEntryWithoutTruncation(t *testing.T) {
	origMax := maxReleaseAssetBytes
	maxReleaseAssetBytes = 1024
	defer func() { maxReleaseAssetBytes = origMax }()

	oversized := bytes.Repeat([]byte("A"), 1025)
	if _, err := extractGS("gs_linux_amd64.tar.gz", tarGz(t, "gs", oversized)); err == nil {
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
	if _, err := extractGS("gs_windows_amd64.zip", zbuf.Bytes()); err == nil {
		t.Fatal("expected zip entry exceeding maxReleaseAssetBytes to fail instead of truncating")
	}
}

func TestUpgradeVerifiesEd25519ChecksumsSignature(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release is a shell script")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubHex := hex.EncodeToString(pub)

	asset, err := releaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported platform for release asset test: %v", err)
	}
	newScript := []byte(fmt.Sprintf("#!/bin/sh\necho '{\"version\":\"%s\"}'\n", "v1.2.3"))
	archive := tarGz(t, "gs", newScript)
	sums := []byte(sumLine(asset, archive))
	validSig := ed25519.Sign(priv, sums)

	var serveSig []byte
	var hasSig bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/releases/latest":
			http.Redirect(w, req, "/releases/tag/v1.2.3", http.StatusFound)
		case "/releases/tag/v1.2.3":
			w.WriteHeader(http.StatusOK)
		case "/releases/download/v1.2.3/" + asset:
			_, _ = w.Write(archive)
		case "/releases/download/v1.2.3/checksums.txt":
			_, _ = w.Write(sums)
		case "/releases/download/v1.2.3/checksums.txt.sig":
			if !hasSig {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(hex.EncodeToString(serveSig)))
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()

	t.Setenv("GS_DOWNLOAD_BASE", srv.URL+"/releases")
	t.Setenv("GS_RELEASE_PUBLIC_KEY", pubHex)

	targetBin := oldBinary(t, "v1.0.0")
	runUpgradeAt := func() error {
		var stdout, stderr bytes.Buffer
		r := Runner{
			Home:   t.TempDir(),
			Stdout: &stdout,
			Stderr: &stderr,
		}
		return r.runUpgrade(context.Background(), commandOptions{Format: "text"}, upgradeRequest{
			version: "v1.2.3",
			path:    targetBin,
		})
	}

	// 1. Missing signature must fail when public key is configured.
	hasSig = false
	if err := runUpgradeAt(); err == nil {
		t.Fatal("expected upgrade to fail when checksums.txt.sig is missing")
	}

	// 2. Tampered signature must fail.
	hasSig = true
	badSig := append([]byte(nil), validSig...)
	badSig[0] ^= 0xff
	serveSig = badSig
	if err := runUpgradeAt(); err == nil {
		t.Fatal("expected upgrade to fail when checksums.txt.sig is invalid")
	}

	// 3. Valid Ed25519 signature succeeds and replaces binary.
	serveSig = validSig
	if err := runUpgradeAt(); err != nil {
		t.Fatalf("expected signed upgrade to succeed, got: %v", err)
	}
	gotBin, err := os.ReadFile(targetBin)
	if err != nil || string(gotBin) != string(newScript) {
		t.Fatalf("upgraded binary = %q, err=%v", string(gotBin), err)
	}
}
