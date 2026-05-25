package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/treestore"
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

func fileEntryFromTree(entry treestore.FileEntry) FileEntry {
	return FileEntry{
		Path:        entry.Path,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Mode:        entry.Mode,
		Size:        entry.Size,
	}
}

func fileEntryToTree(entry FileEntry) treestore.FileEntry {
	return treestore.FileEntry{
		Path:        entry.Path,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Mode:        entry.Mode,
		Size:        entry.Size,
	}
}

func treeEntryFromTree(entry treestore.TreeEntry) TreeEntry {
	return TreeEntry{
		Path:        entry.Path,
		Name:        entry.Name,
		Kind:        entry.Kind,
		Mode:        entry.Mode,
		TreeID:      entry.TreeID,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Size:        entry.Size,
	}
}

func MissingEntryFingerprint() string {
	return "missing"
}

func normalizeDevSubject(devUser string) string {
	devUser = strings.TrimSpace(devUser)
	if devUser == "" {
		devUser = "alice"
	}
	devUser = strings.ReplaceAll(devUser, "-", "_")
	if strings.HasPrefix(devUser, "user_") || strings.HasSuffix(devUser, "_bot") {
		return devUser
	}
	return "user_" + devUser
}

func normalizeSignupUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	username = strings.ReplaceAll(username, "_", "-")
	if username == "" {
		return "", fmt.Errorf("username is required")
	}
	if len(username) > 63 {
		return "", fmt.Errorf("username must be 63 characters or fewer")
	}
	if strings.HasPrefix(username, "-") || strings.HasSuffix(username, "-") {
		return "", fmt.Errorf("username must not start or end with '-'")
	}
	for _, r := range username {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", fmt.Errorf("username may contain only letters, numbers, '-' or '_'")
	}
	return username, nil
}

func signupSubjectID(username string) string {
	return "user_" + strings.ReplaceAll(username, "-", "_")
}

func signupAccountID(username string) string {
	return "acct_" + strings.ReplaceAll(username, "-", "_")
}

func signupHomeSliceID(username string) string {
	return "slice_" + strings.ReplaceAll(username, "-", "_") + "_home"
}

func sliceID(account, slug string) string {
	return "slice_" + strings.ReplaceAll(account, "-", "_") + "_" + strings.ReplaceAll(slug, "-", "_")
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func definitionHash(sliceID string, version int64, included []string, visibility string) string {
	payload, _ := json.Marshal(struct {
		SliceID    string   `json:"slice_id"`
		Version    int64    `json:"version"`
		Included   []string `json:"included_paths"`
		Visibility string   `json:"visibility"`
	}{SliceID: sliceID, Version: version, Included: included, Visibility: visibility})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func encodeJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func decodeJSON(raw []byte, v any) error {
	if len(raw) == 0 {
		raw = []byte("null")
	}
	return json.Unmarshal(raw, v)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
