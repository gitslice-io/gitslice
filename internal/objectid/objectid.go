package objectid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const algorithm = "sha256"

func RawContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return algorithm + ":" + hex.EncodeToString(sum[:])
}

func BlobID(data []byte) string {
	return hash("gitslice.blob.v1", data)
}

type TreeEntry struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Mode          uint32 `json:"mode,omitempty"`
	TreeID        string `json:"tree_id,omitempty"`
	BlobID        string `json:"blob_id,omitempty"`
	SymlinkTarget string `json:"symlink_target,omitempty"`
	ContentHash   string `json:"content_hash,omitempty"`
}

func TreeID(entries []TreeEntry) string {
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
	return algorithm + ":" + hex.EncodeToString(h.Sum(nil))
}
