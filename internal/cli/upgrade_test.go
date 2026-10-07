package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
		if got, err := releaseAsset(tc.goos, tc.goarch); err != nil || got != tc.want {
			t.Fatalf("releaseAsset(%s, %s) = %q, %v", tc.goos, tc.goarch, got, err)
		}
	}
	if _, err := releaseAsset("plan9", "amd64"); err == nil {
		t.Fatal("plan9 has no release")
	}

	archive := tarGz(t, "gs", []byte("binary"))
	if got, err := extractGS("gs_linux_amd64.tar.gz", archive); err != nil || string(got) != "binary" {
		t.Fatalf("extract tar.gz = %q, %v", got, err)
	}
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("gs.exe")
	_, _ = w.Write([]byte("exe"))
	_ = zw.Close()
	if got, err := extractGS("gs_windows_amd64.zip", zbuf.Bytes()); err != nil || string(got) != "exe" {
		t.Fatalf("extract zip = %q, %v", got, err)
	}
	if _, err := extractGS("gs_linux_amd64.tar.gz", tarGz(t, "README", []byte("x"))); err == nil {
		t.Fatal("an archive without gs must be refused")
	}

	sums := []byte(sumLine("other.tar.gz", []byte("o")) + sumLine("gs_linux_amd64.tar.gz", archive))
	if err := verifyChecksum(sums, "gs_linux_amd64.tar.gz", archive); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(sums, "gs_linux_amd64.tar.gz", []byte("tampered")); err == nil {
		t.Fatal("a tampered download must be refused")
	}
	if err := verifyChecksum(sums, "gs_linux_arm64.tar.gz", archive); err == nil {
		t.Fatal("an asset without a checksum must be refused")
	}
}

func TestVersionComparison(t *testing.T) {
	if !sameVersion("v0.4.0", "0.4.0") || sameVersion("v0.4.0", "v0.4.1") || sameVersion("", "") {
		t.Fatal("sameVersion")
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
		if got := newerVersion(tc.a, tc.b); got != tc.want {
			t.Fatalf("newerVersion(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

// releaseServer serves a fake release v9.9.9 whose gs is a shell script that
// reports reportVersion, the way the release workflow lays the files out.
func releaseServer(t *testing.T, reportVersion string, tamper bool) string {
	t.Helper()
	asset, err := releaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	script := []byte(fmt.Sprintf("#!/bin/sh\necho '{\"version\":\"%s\"}'\n", reportVersion))
	archive := tarGz(t, "gs", script)
	sums := sumLine(asset, archive)
	if tamper {
		archive = tarGz(t, "gs", []byte("#!/bin/sh\necho evil\n"))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/v9.9.9", http.StatusFound)
	})
	mux.HandleFunc("/releases/tag/v9.9.9", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/releases/download/v9.9.9/"+asset, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/releases/download/v9.9.9/checksums.txt", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(sums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/releases"
}

// oldBinary is an installed gs that reports version.
func oldBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gs")
	if err := os.WriteFile(path, []byte(oldScript(version)), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func oldScript(version string) string {
	return fmt.Sprintf("#!/bin/sh\n# old gs\necho '{\"version\":\"%s\"}'\n", version)
}

func TestUpgradeReplacesTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release is a shell script")
	}
	t.Setenv("GS_DOWNLOAD_BASE", releaseServer(t, "v9.9.9", false))
	path := oldBinary(t, "v1.2.3")
	var out bytes.Buffer
	r := Runner{Stdout: &out, Stderr: &out}

	// --check reports without touching anything.
	if err := r.runUpgrade(context.Background(), commandOptions{Format: "text"}, upgradeRequest{check: true, path: path}); err != nil {
		t.Fatal(err)
	}
	// It reports the version of the binary at --path, not of this gs.
	if !strings.Contains(out.String(), "gs v1.2.3 is installed; v9.9.9 is available") {
		t.Fatalf("check output: %s", out.String())
	}
	if data, _ := os.ReadFile(path); string(data) != oldScript("v1.2.3") {
		t.Fatal("--check must not install")
	}

	out.Reset()
	if err := r.runUpgrade(context.Background(), commandOptions{Format: "json"}, upgradeRequest{path: path}); err != nil {
		t.Fatal(err)
	}
	var result upgradeOutput
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("json output %q: %v", out.String(), err)
	}
	if !result.Upgraded || result.Current != "v1.2.3" || result.Target != "v9.9.9" || result.Path != path {
		t.Fatalf("result = %+v", result)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "v9.9.9") {
		t.Fatalf("binary not replaced: %q", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm()&0o100 == 0 {
		t.Fatal("the new gs must be executable")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".gs-upgrade-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestUpgradeLeavesTheOldBinaryOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release is a shell script")
	}
	for name, base := range map[string]string{
		"tampered download":   releaseServer(t, "v9.9.9", true),
		"wrong version built": releaseServer(t, "v1.0.0", false),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GS_DOWNLOAD_BASE", base)
			path := oldBinary(t, "v1.2.3")
			r := Runner{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
			if err := r.runUpgrade(context.Background(), commandOptions{Format: "text"}, upgradeRequest{path: path}); err == nil {
				t.Fatal("the upgrade should fail")
			}
			if data, _ := os.ReadFile(path); string(data) != oldScript("v1.2.3") {
				t.Fatalf("old gs changed: %q", data)
			}
		})
	}
}

func TestUpgradeSkipsWhenCurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gs is a shell script")
	}
	t.Setenv("GS_DOWNLOAD_BASE", releaseServer(t, "v9.9.9", false))
	path := oldBinary(t, "v9.9.9")
	var out bytes.Buffer
	r := Runner{Stdout: &out, Stderr: &out}
	if err := r.runUpgrade(context.Background(), commandOptions{Format: "text"}, upgradeRequest{path: path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gs v9.9.9 is already installed") {
		t.Fatalf("output: %s", out.String())
	}
	if data, _ := os.ReadFile(path); string(data) != oldScript("v9.9.9") {
		t.Fatal("nothing should be installed when current")
	}
}

func TestUpgradeSaysDowngradedAndReinstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release is a shell script")
	}
	t.Setenv("GS_DOWNLOAD_BASE", releaseServer(t, "v9.9.9", false))
	for _, tc := range []struct {
		installed string
		force     bool
		want      string
	}{
		{"v10.0.0", false, "downgraded gs v10.0.0 -> v9.9.9"},
		{"v9.9.9", true, "reinstalled gs v9.9.9 -> v9.9.9"},
		{"v1.0.0", false, "upgraded gs v1.0.0 -> v9.9.9"},
	} {
		path := oldBinary(t, tc.installed)
		var out bytes.Buffer
		r := Runner{Stdout: &out, Stderr: &out}
		if err := r.runUpgrade(context.Background(), commandOptions{Format: "text"}, upgradeRequest{path: path, version: "v9.9.9", force: tc.force}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("installed %s: output %q, want %q", tc.installed, out.String(), tc.want)
		}
	}
}
