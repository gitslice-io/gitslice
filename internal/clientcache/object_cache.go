package clientcache

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gitslice-io/gitslice/internal/objectid"
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
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o700); err != nil {
		return nil, err
	}
	return cache, nil
}

func (c *ObjectCache) Root() string {
	return c.root
}

func (c *ObjectCache) PutBytes(data []byte) (Object, error) {
	return c.PutReader(bytes.NewReader(data))
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
	if err := os.MkdirAll(filepath.Join(c.root, "tmp"), 0o700); err != nil {
		return Object{}, err
	}
	tmp, err := os.CreateTemp(filepath.Join(c.root, "tmp"), "object-*")
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
	if _, err := os.Stat(target); err == nil {
		return Object{ContentHash: contentHash, Size: size}, nil
	} else if !os.IsNotExist(err) {
		return Object{}, err
	}
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
	removeTmp = false
	_ = os.Chmod(target, 0o400)
	return Object{ContentHash: contentHash, Size: size}, nil
}

func (c *ObjectCache) Read(contentHash string) ([]byte, error) {
	p, err := c.Path(contentHash)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (c *ObjectCache) Open(contentHash string) (*os.File, error) {
	p, err := c.Path(contentHash)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (c *ObjectCache) Exists(contentHash string) bool {
	p, err := c.Path(contentHash)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
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
