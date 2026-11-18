package clientcache

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/objectid"
)

type Object struct {
	ContentHash string
	Size        int64
}

type ObjectCache struct {
	root string
}

func New(root string) (*ObjectCache, error) {
	if root == "" {
		return nil, fmt.Errorf("client object cache root is required")
	}
	cache := &ObjectCache{root: root}
	if err := cache.EnsureDir(); err != nil {
		return nil, err
	}
	return cache, nil
}

// EnsureDir creates and tightens the cache root and internal directories to 0700.
func (c *ObjectCache) EnsureDir() error {
	for _, dir := range []string{
		c.root,
		filepath.Join(c.root, "objects"),
		filepath.Join(c.root, "tmp"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (c *ObjectCache) Root() string {
	return c.root
}

// Empty reports whether the cache holds no objects (a fresh machine or
// sandbox), from a single directory read.
func (c *ObjectCache) Empty() bool {
	f, err := os.Open(filepath.Join(c.root, "objects"))
	if err != nil {
		return true
	}
	defer f.Close()
	names, _ := f.Readdirnames(1)
	return len(names) == 0
}

func (c *ObjectCache) PutBytes(data []byte) (Object, error) {
	contentHash := objectid.RawContentHash(data)
	size := int64(len(data))
	if c.Exists(contentHash) {
		return Object{ContentHash: contentHash, Size: size}, nil
	}
	target, err := c.Path(contentHash)
	if err != nil {
		return Object{}, err
	}
	tmpDir := filepath.Join(c.root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return Object{}, err
	}
	_ = os.Chmod(tmpDir, 0o700)
	tmp, err := os.CreateTemp(tmpDir, "object-*")
	if err != nil {
		return Object{}, err
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return Object{}, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return Object{}, err
	}
	if err := tmp.Close(); err != nil {
		return Object{}, err
	}
	return c.commitTempObject(tmpName, target, contentHash, size, &removeTmp)
}

func (c *ObjectCache) PutFile(path string) (Object, error) {
	f, err := os.Open(path)
	if err != nil {
		return Object{}, err
	}
	defer f.Close()
	return c.PutReader(f)
}

func (c *ObjectCache) PutReader(r io.Reader) (Object, error) {
	tmpDir := filepath.Join(c.root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return Object{}, err
	}
	_ = os.Chmod(tmpDir, 0o700)
	tmp, err := os.CreateTemp(tmpDir, "object-*")
	if err != nil {
		return Object{}, err
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()

	contentHash, size, err := objectid.RawContentHashReader(io.TeeReader(r, tmp))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Object{}, err
	}

	target, err := c.Path(contentHash)
	if err != nil {
		return Object{}, err
	}
	if info, err := os.Stat(target); err == nil {
		touch(target, info)
		return Object{ContentHash: contentHash, Size: size}, nil
	} else if !os.IsNotExist(err) {
		return Object{}, err
	}
	return c.commitTempObject(tmpName, target, contentHash, size, &removeTmp)
}

func (c *ObjectCache) commitTempObject(tmpName, target, contentHash string, size int64, removeTmp *bool) (Object, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return Object{}, err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return Object{}, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		if _, statErr := os.Stat(target); statErr == nil {
			return Object{ContentHash: contentHash, Size: size}, nil
		}
		return Object{}, err
	}
	*removeTmp = false
	_ = os.Chmod(target, 0o400)
	return Object{ContentHash: contentHash, Size: size}, nil
}

func (c *ObjectCache) Read(contentHash string) ([]byte, error) {
	f, err := c.Open(contentHash)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Open opens an object, marking it as used (see Prune).
func (c *ObjectCache) Open(contentHash string) (*os.File, error) {
	p, err := c.Path(contentHash)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err == nil {
		touch(p, info)
	}
	return f, nil
}

// Exists reports whether the cache has an object, marking it as used (see
// Prune): a caller that checks is about to read it, or to rely on it.
func (c *ObjectCache) Exists(contentHash string) bool {
	p, err := c.Path(contentHash)
	if err != nil {
		return false
	}
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	touch(p, info)
	return true
}

func (c *ObjectCache) Path(contentHash string) (string, error) {
	rel, err := ObjectPath(contentHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.root, rel), nil
}

func ObjectPath(contentHash string) (string, error) {
	algorithm, digest, ok := strings.Cut(contentHash, ":")
	if !ok || algorithm != "sha256" || len(digest) != 64 {
		return "", fmt.Errorf("invalid content hash %q", contentHash)
	}
	for _, ch := range digest {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return "", fmt.Errorf("invalid content hash %q", contentHash)
		}
	}
	return filepath.Join("objects", "sha256", digest[:2], digest[2:4], digest), nil
}
