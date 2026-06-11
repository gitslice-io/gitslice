package objectid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const algorithm = "sha256"

func RawContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return formatHash(sum[:])
}

func RawContentHashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return formatHash(h.Sum(nil)), n, nil
}

func BlobID(data []byte) string {
	h := NewBlobHasher()
	_, _ = h.Write(data)
	return h.BlobID()
}

type BlobHasher struct {
	raw  hashWriter
	blob hashWriter
	size int64
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func NewBlobHasher() *BlobHasher {
	blob := sha256.New()
	blob.Write([]byte("gitslice.blob.v1"))
	blob.Write([]byte{0})
	return &BlobHasher{
		raw:  sha256.New(),
		blob: blob,
	}
}

func (h *BlobHasher) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	_, _ = h.raw.Write(p)
	_, _ = h.blob.Write(p)
	h.size += int64(len(p))
	return len(p), nil
}

func (h *BlobHasher) RawContentHash() string {
	return formatHash(h.raw.Sum(nil))
}

func (h *BlobHasher) BlobID() string {
	return formatHash(h.blob.Sum(nil))
}

func (h *BlobHasher) Size() int64 {
	return h.size
}

type TreeEntry struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Mode          uint32 `json:"mode,omitempty"`
	TreeID        string `json:"tree_id,omitempty"`
	BlobID        string `json:"blob_id,omitempty"`
	SymlinkTarget string `json:"symlink_target,omitempty"`
	Size          int64  `json:"size,omitempty"`
	ContentHash   string `json:"content_hash,omitempty"`
}

func TreeID(entries []TreeEntry) string {
	if entries == nil {
		entries = []TreeEntry{}
	}
	payload, _ := json.Marshal(struct {
		Entries []TreeEntry `json:"entries"`
	}{Entries: entries})
	return hash("gitslice.tree.v1", payload)
}

func EmptyTreeID() string {
	return TreeID(nil)
}

type CommitObject struct {
	ParentIDs    []string  `json:"parent_ids"`
	RootTreeID   string    `json:"root_tree_id"`
	Author       string    `json:"author"`
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"created_at"`
	ChangedPaths []string  `json:"changed_paths"`
}

func CommitID(obj CommitObject) string {
	if obj.ParentIDs == nil {
		obj.ParentIDs = []string{}
	}
	if obj.ChangedPaths == nil {
		obj.ChangedPaths = []string{}
	}
	obj.CreatedAt = obj.CreatedAt.UTC()
	payload, _ := json.Marshal(obj)
	return hash("gitslice.commit.v1", payload)
}

func RandomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b[:])), nil
}

func hash(domain string, payload []byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(payload)
	return formatHash(h.Sum(nil))
}

func formatHash(sum []byte) string {
	return algorithm + ":" + hex.EncodeToString(sum)
}
