package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/selfupdate"
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
		if err := selfupdate.ValidateReleaseTag(invalid); err == nil {
			t.Fatalf("ValidateReleaseTag(%q) unexpectedly succeeded", invalid)
		}
	}
	for _, valid := range []string{
		"v0.4.2",
		"v1.0.0-rc.1",
	} {
		if err := selfupdate.ValidateReleaseTag(valid); err != nil {
			t.Fatalf("ValidateReleaseTag(%q) err=%v", valid, err)
		}
	}

	if _, err := selfupdate.ValidateReleaseBaseURL("http://releases.evil.example/gs"); err == nil {
		t.Fatal("expected plaintext non-loopback GS_DOWNLOAD_BASE to be rejected")
	}
	if _, err := selfupdate.ValidateReleaseBaseURL("http://127.0.0.1:8080/releases"); err != nil {
		t.Fatalf("expected loopback HTTP test base to be allowed, got: %v", err)
	}
	if _, err := selfupdate.ValidateReleaseBaseURL("https://gitslice.io/releases/"); err != nil {
		t.Fatalf("expected HTTPS release base to be allowed, got: %v", err)
	}

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

func TestUpgradeVerifiesEd25519ChecksumsSignature(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release is a shell script")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubHex := hex.EncodeToString(pub)

	asset, err := selfupdate.ReleaseAsset(runtime.GOOS, runtime.GOARCH)
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

	hasSig = false
	if err := runUpgradeAt(); err == nil {
		t.Fatal("expected upgrade to fail when checksums.txt.sig is missing")
	}

	hasSig = true
	badSig := append([]byte(nil), validSig...)
	badSig[0] ^= 0xff
	serveSig = badSig
	if err := runUpgradeAt(); err == nil {
		t.Fatal("expected upgrade to fail when checksums.txt.sig is invalid")
	}

	serveSig = validSig
	if err := runUpgradeAt(); err != nil {
		t.Fatalf("expected signed upgrade to succeed, got: %v", err)
	}
	gotBin, err := os.ReadFile(targetBin)
	if err != nil || string(gotBin) != string(newScript) {
		t.Fatalf("upgraded binary = %q, err=%v", string(gotBin), err)
	}
}
