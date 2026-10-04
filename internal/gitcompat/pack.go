package gitcompat

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" //nolint:gosec // Git pack checksums are SHA-1.
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/objectid"
)

// packBuilder writes a Git packfile of whole (not deltified) objects to a
// temporary file and has git index it into a repository. A pack is the cheapest
// way to add many objects to a repository, and it is the unit the pack mirror
// copies to the object store.
//
// Trees and commits are added from memory. Blob contents are streamed through
// zlib, so a large file never has to be held in memory.
type packBuilder struct {
	file  *os.File
	count uint32
	size  int64
}

func newPackBuilder(dir string) (*packBuilder, error) {
	f, err := os.CreateTemp(dir, ".pack-*")
	if err != nil {
		return nil, err
	}
	// Reserve the header; the object count is known only at the end.
	if _, err := f.Write(make([]byte, 12)); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return &packBuilder{file: f, size: 12}, nil
}

// Objects reports how many objects have been added.
func (p *packBuilder) Objects() int { return int(p.count) }

// Size reports the bytes written so far.
func (p *packBuilder) Size() int64 { return p.size }

func (p *packBuilder) header(kind byte, size int64) error {
	b := []byte{kind<<4 | byte(size&0x0f)}
	size >>= 4
	for size > 0 {
		b[len(b)-1] |= 0x80
		b = append(b, byte(size&0x7f))
		size >>= 7
	}
	n, err := p.file.Write(b)
	p.size += int64(n)
	return err
}

// add appends one object whose bytes are in memory.
func (p *packBuilder) add(kind byte, data []byte) error {
	if err := p.header(kind, int64(len(data))); err != nil {
		return err
	}
	cw := &countingWriter{w: p.file}
	zw := getZlib(cw)
	defer putZlib(zw)
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	p.size += cw.n
	p.count++
	return nil
}

// addBlob appends a blob read from r, which must hold exactly size bytes. It
// returns the blob's Git id, computed on the way through.
func (p *packBuilder) addBlob(size int64, r io.Reader) (string, error) {
	if err := p.header(objBlob, size); err != nil {
		return "", err
	}
	cw := &countingWriter{w: p.file}
	zw := getFastZlib(cw)
	defer putFastZlib(zw)
	hasher := objectid.NewGitBlobHasher(size)
	if _, err := io.Copy(io.MultiWriter(zw, hasher), r); err != nil {
		return "", err
	}
	if !hasher.Complete() {
		return "", fmt.Errorf("blob should hold %d bytes but held a different number", size)
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	p.size += cw.n
	p.count++
	return hasher.ID(), nil
}

// A zlib writer holds about a megabyte of state. A build writes one per object,
// tens of thousands of them, so allocating each made the garbage collector the
// largest cost of a build; they are reused instead.
var zlibWriters = sync.Pool{New: func() any { return zlib.NewWriter(io.Discard) }}

// File contents are compressed at the fastest level: they are the bulk of the
// bytes, much of it already compressed, and the pack is a cache the object
// store can always refill.
var zlibFastWriters = sync.Pool{New: func() any {
	zw, _ := zlib.NewWriterLevel(io.Discard, zlib.BestSpeed)
	return zw
}}

func getZlib(w io.Writer) *zlib.Writer {
	zw := zlibWriters.Get().(*zlib.Writer)
	zw.Reset(w)
	return zw
}

func putZlib(zw *zlib.Writer) { zlibWriters.Put(zw) }

func getFastZlib(w io.Writer) *zlib.Writer {
	zw := zlibFastWriters.Get().(*zlib.Writer)
	zw.Reset(w)
	return zw
}

func putFastZlib(zw *zlib.Writer) { zlibFastWriters.Put(zw) }

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// discard throws the pack away.
func (p *packBuilder) discard() {
	_ = p.file.Close()
	_ = os.Remove(p.file.Name())
}

// finish completes the pack, has git index it into repoPath, and returns the
// pack's name ("pack-<hash>"). It does nothing and returns "" if the pack is
// empty.
func (p *packBuilder) finish(ctx context.Context, repoPath string) (string, error) {
	defer p.discard()
	if p.count == 0 {
		return "", nil
	}
	var header [12]byte
	copy(header[:], "PACK")
	binary.BigEndian.PutUint32(header[4:], 2)
	binary.BigEndian.PutUint32(header[8:], p.count)
	if _, err := p.file.WriteAt(header[:], 0); err != nil {
		return "", err
	}
	// The trailer is the SHA-1 of everything before it.
	sum := sha1.New() //nolint:gosec // see the import
	if _, err := p.file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if _, err := io.Copy(sum, p.file); err != nil {
		return "", err
	}
	if _, err := p.file.Write(sum.Sum(nil)); err != nil {
		return "", err
	}
	if _, err := p.file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "index-pack", "--stdin")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoPath)
	cmd.Stdin = p.file
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git index-pack failed: %w\n%s", err, stderr.String())
	}
	fields := strings.Fields(stdout.String())
	if len(fields) < 2 {
		return "", errors.New("git index-pack did not name the pack")
	}
	return "pack-" + fields[len(fields)-1], nil
}

// listPacks returns the names ("pack-<hash>") of the packs in a repository.
func listPacks(repoPath string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(repoPath, "objects", "pack"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".pack"); ok && strings.HasPrefix(name, "pack-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
