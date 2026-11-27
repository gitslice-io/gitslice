package postgres

import (
	"encoding/json"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	"gitslice.io/gitslice/internal/usernames"
)

func FileEntryFingerprint(entry FileEntry) string {
	return storage.FileEntryFingerprint(entry)
}

func DirectoryEntryFingerprint(treeID string) string {
	return storage.DirectoryEntryFingerprint(treeID)
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
	return storage.MissingEntryFingerprint()
}

func normalizeSignupUsername(username string) (string, error) {
	return usernames.Normalize(username)
}

func signupAccountID(username string) string {
	return "acct_" + strings.ReplaceAll(username, "-", "_")
}

func tokenHash(token string) string {
	return storage.TokenHash(token)
}

func definitionHash(sliceID string, version int64, included []string, visibility string, requiredApprovals int32, requiredChecks []string) string {
	return storage.DefinitionHash(sliceID, version, included, visibility, requiredApprovals, 0, requiredChecks, "")
}

func definitionHashWithApprovers(sliceID string, version int64, included []string, visibility string, requiredApprovals, requiredHumanApprovals int32, requiredChecks []string, approvers string) string {
	return storage.DefinitionHash(sliceID, version, included, visibility, requiredApprovals, requiredHumanApprovals, requiredChecks, approvers)
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
