package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// InstallRelease downloads tag from base, verifies checksums (and optional
// Ed25519 signature), smoke-tests the extracted binary, and atomically replaces
// binary.
func InstallRelease(ctx context.Context, client *http.Client, base, tag, binary, fallbackPubKey string) error {
	if err := ValidateReleaseTag(tag); err != nil {
		return &Error{Code: "invalid_version", Message: fmt.Sprintf("invalid release tag %q: %v", tag, err)}
	}
	pubKey, requireSig, err := configuredReleasePublicKey(fallbackPubKey)
	if err != nil {
		return &Error{Code: "invalid_release_key", Message: fmt.Sprintf("invalid release public key: %v", err)}
	}
	asset, err := ReleaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return &Error{Code: "unsupported_platform", Message: err.Error(), Hint: "Build gs from source: go install gitslice.io/gitslice/cmd/gs@latest"}
	}
	dir := base + "/download/" + tag
	sums, err := download(ctx, client, dir+"/checksums.txt")
	if err != nil {
		return &Error{Code: "download_failed", Message: fmt.Sprintf("download %s checksums: %v", tag, err)}
	}
	if requireSig {
		rawSig, err := downloadBounded(ctx, client, dir+"/checksums.txt.sig", maxReleaseSigBytes)
		if err != nil {
			return &Error{
				Code:    "signature_mismatch",
				Message: fmt.Sprintf("download %s checksums signature: %v", tag, err),
				Hint:    "The release checksums signature could not be verified; nothing was installed.",
			}
		}
		if err := verifyReleaseChecksumsSignature(pubKey, sums, rawSig); err != nil {
			return &Error{
				Code:    "signature_mismatch",
				Message: err.Error(),
				Hint:    "The release checksums signature could not be verified; nothing was installed.",
			}
		}
	}
	archive, err := download(ctx, client, dir+"/"+asset)
	if err != nil {
		return &Error{Code: "download_failed", Message: fmt.Sprintf("download %s %s: %v", tag, asset, err), Hint: "Check the version with gs upgrade --check."}
	}
	if err := VerifyChecksum(sums, asset, archive); err != nil {
		return &Error{Code: "checksum_mismatch", Message: err.Error(), Hint: "The download was damaged or tampered with; nothing was installed."}
	}
	exe, err := ExtractGS(asset, archive)
	if err != nil {
		return &Error{Code: "bad_release", Message: err.Error()}
	}
	return replaceBinary(binary, exe, tag)
}

func configuredReleasePublicKey(fallback string) (ed25519.PublicKey, bool, error) {
	raw := strings.TrimSpace(os.Getenv("GS_RELEASE_PUBLIC_KEY"))
	if raw == "" {
		raw = strings.TrimSpace(ReleasePublicKey)
	}
	if raw == "" {
		raw = strings.TrimSpace(fallback)
	}
	if raw == "" {
		return nil, false, nil
	}
	decoded, err := decodeHexOrBase64(raw)
	if err != nil {
		return nil, false, err
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, false, fmt.Errorf("expected %d-byte Ed25519 public key, got %d bytes", ed25519.PublicKeySize, len(decoded))
	}
	return ed25519.PublicKey(decoded), true, nil
}

func verifyReleaseChecksumsSignature(pubKey ed25519.PublicKey, sums, rawSig []byte) error {
	sig, err := parseReleaseSignature(rawSig)
	if err != nil {
		return fmt.Errorf("invalid checksums.txt.sig: %w", err)
	}
	if !ed25519.Verify(pubKey, sums, sig) {
		return errors.New("checksums.txt signature verification failed")
	}
	return nil
}

func parseReleaseSignature(raw []byte) ([]byte, error) {
	if len(raw) == ed25519.SignatureSize {
		return raw, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("empty signature")
	}
	decoded, err := decodeHexOrBase64(trimmed)
	if err != nil {
		return nil, err
	}
	if len(decoded) != ed25519.SignatureSize {
		return nil, fmt.Errorf("expected %d-byte Ed25519 signature, got %d bytes", ed25519.SignatureSize, len(decoded))
	}
	return decoded, nil
}

func decodeHexOrBase64(s string) ([]byte, error) {
	if decoded, err := hex.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	return nil, errors.New("must be hex or base64 encoded")
}

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	return downloadBounded(ctx, client, url, maxReleaseAssetBytes)
}

func downloadBounded(ctx context.Context, client *http.Client, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("download is too large")
	}
	return data, nil
}

// VerifyChecksum checks data against its line in a sha256sum-style checksums.txt.
func VerifyChecksum(sums []byte, asset string, data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			sum := sha256.Sum256(data)
			if !strings.EqualFold(fields[0], hex.EncodeToString(sum[:])) {
				return fmt.Errorf("checksum mismatch for %s", asset)
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum for %s", asset)
}

// ExtractGS returns the gs (or gs.exe) executable from a release archive.
func ExtractGS(asset string, archive []byte) ([]byte, error) {
	limit := maxReleaseAssetBytes
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == "gs.exe" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				data, err := io.ReadAll(io.LimitReader(rc, limit+1))
				if err != nil {
					return nil, err
				}
				if int64(len(data)) > limit {
					return nil, fmt.Errorf("release binary in %s exceeds maximum size", asset)
				}
				return data, nil
			}
		}
		return nil, fmt.Errorf("%s has no gs.exe", asset)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s has no gs", asset)
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == "gs" {
			data, err := io.ReadAll(io.LimitReader(tr, limit+1))
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > limit {
				return nil, fmt.Errorf("release binary in %s exceeds maximum size", asset)
			}
			return data, nil
		}
	}
}

func replaceBinary(binary string, exe []byte, tag string) error {
	dir := filepath.Dir(binary)
	tmp, err := os.CreateTemp(dir, ".gs-upgrade-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return &Error{
				Code:    "permission_denied",
				Message: fmt.Sprintf("cannot write to %s", dir),
				Hint:    "Run gs upgrade as the user that installed gs, or reinstall with GS_INSTALL_DIR set (see https://gitslice.io/install.sh).",
			}
		}
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(exe); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := checkNewBinary(tmpPath, tag); err != nil {
		return &Error{Code: "bad_release", Message: err.Error(), Hint: "Nothing was installed."}
	}
	if runtime.GOOS == "windows" {
		old := binary + ".old"
		_ = os.Remove(old)
		if err := os.Rename(binary, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(tmpPath, binary)
}

func checkNewBinary(binary, tag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version", "--json").Output()
	if err != nil {
		return fmt.Errorf("the downloaded gs does not run: %v", err)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return fmt.Errorf("the downloaded gs printed an unexpected version: %v", err)
	}
	if !SameVersion(info.Version, tag) {
		return fmt.Errorf("the downloaded gs is %s, not %s", info.Version, tag)
	}
	return nil
}
