package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxExtractFiles = 20_000
	maxExtractBytes = 256 << 20
)

func downloadAndExtractArchive(ctx context.Context, client *http.Client, bridge, destRoot string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bridge+"/archive", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return extractTarGz(resp.Body, destRoot)
}

func extractTarGz(r io.Reader, destRoot string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	cleanRoot := filepath.Clean(destRoot)
	tr := tar.NewReader(gz)
	var files int
	var totalBytes int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			// Refuse symlinks, hardlinks, device nodes, FIFOs.
			continue
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if name == "" || strings.HasPrefix(name, "/") {
			return fmt.Errorf("archive entry %q has unsafe path", hdr.Name)
		}
		target := filepath.Join(cleanRoot, filepath.FromSlash(name))
		rel, err := filepath.Rel(cleanRoot, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive entry %q escapes checkout root", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		files++
		if files > maxExtractFiles {
			return fmt.Errorf("archive exceeds %d files", maxExtractFiles)
		}
		if hdr.Size < 0 || totalBytes+hdr.Size > maxExtractBytes {
			return fmt.Errorf("archive exceeds %d MiB", maxExtractBytes>>20)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode & 0o777)
		if mode == 0 {
			mode = 0o644
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		written, copyErr := io.CopyN(f, tr, hdr.Size)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		totalBytes += written
	}
}
