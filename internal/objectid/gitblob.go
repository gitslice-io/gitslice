package objectid

import (
	"crypto/sha1" //nolint:gosec // Git's blob id is SHA-1; this is the id Git clients expect, not an integrity check.
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
)

// GitBlobHasher computes the id Git gives a file: the SHA-1 of "blob <size>"
// and a NUL byte, followed by the file's bytes. Because the size comes first,
// it must be known before the first byte is written.
//
// Gitslice's own ids are SHA-256 and stay the identity of the data. The Git id
// is kept next to them so that Git trees and commits for a slice can be built
// from metadata, without reading every file.
type GitBlobHasher struct {
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	want    int64
	written int64
}

func NewGitBlobHasher(size int64) *GitBlobHasher {
	h := sha1.New() //nolint:gosec // see the import
	_, _ = h.Write([]byte("blob " + strconv.FormatInt(size, 10) + "\x00"))
	return &GitBlobHasher{h: h, want: size}
}

func (g *GitBlobHasher) Write(p []byte) (int, error) {
	_, _ = g.h.Write(p)
	g.written += int64(len(p))
	return len(p), nil
}

// Complete reports whether exactly the declared number of bytes were written.
func (g *GitBlobHasher) Complete() bool { return g.written == g.want }

// ID returns the 40-character hex id.
func (g *GitBlobHasher) ID() string { return hex.EncodeToString(g.h.Sum(nil)) }

// GitBlobID returns the Git blob id of data.
func GitBlobID(data []byte) string {
	g := NewGitBlobHasher(int64(len(data)))
	_, _ = g.Write(data)
	return g.ID()
}

// GitBlobIDReader returns the Git blob id of size bytes read from r. It fails
// if r holds a different number of bytes.
func GitBlobIDReader(size int64, r io.Reader) (string, error) {
	g := NewGitBlobHasher(size)
	if _, err := io.Copy(g, r); err != nil {
		return "", err
	}
	if !g.Complete() {
		return "", fmt.Errorf("expected %d bytes, read %d", size, g.written)
	}
	return g.ID(), nil
}
