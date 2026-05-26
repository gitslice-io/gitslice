package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func FileEntryFingerprint(entry FileEntry) string {
	payload, _ := json.Marshal(struct {
		Kind        string `json:"kind"`
		Mode        uint32 `json:"mode"`
		BlobID      string `json:"blob_id"`
		ContentHash string `json:"content_hash"`
	}{
		Kind:        "file",
		Mode:        entry.Mode,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
	})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DirectoryEntryFingerprint(treeID string) string {
	payload, _ := json.Marshal(struct {
		Kind   string `json:"kind"`
		TreeID string `json:"tree_id"`
	}{
		Kind:   "directory",
		TreeID: treeID,
	})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func MissingEntryFingerprint() string {
	return "missing"
}
