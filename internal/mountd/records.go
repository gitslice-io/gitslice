package mountd

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Record is a lazy workspace's mount.json.
type Record struct {
	ID             string   `json:"id"`
	Account        string   `json:"account"`
	Slice          string   `json:"slice"`
	MountPath      string   `json:"mount_path"`
	ServerAddr     string   `json:"server_addr"`
	BaseCommit     string   `json:"base_commit"`
	Files          int      `json:"files"`
	CreatedAt      string   `json:"created_at"`
	MountedAt      string   `json:"mounted_at,omitempty"`
	SliceID        string   `json:"slice_id,omitempty"`
	DefinitionHash string   `json:"definition_hash,omitempty"`
	IncludedPaths  []string `json:"included_paths,omitempty"`
	ReadOnly       bool     `json:"read_only,omitempty"`
	ConversationID string   `json:"conversation_id,omitempty"`
	Prefetch       []string `json:"prefetch,omitempty"`
}

func (rec Record) Ref() *corev1.SliceRef {
	return &corev1.SliceRef{Account: rec.Account, Slice: rec.Slice}
}

// PrefetchEntries returns the base files matching patterns that cache lacks.
func PrefetchEntries(manifest *lazyfs.Manifest, patterns []string, cache *clientcache.ObjectCache) []lazyfs.BaseEntry {
	var out []lazyfs.BaseEntry
	seen := map[string]bool{}
	manifest.Files(func(entry lazyfs.BaseEntry) {
		if entry.Kind != lazyfs.KindFile || seen[entry.ContentHash] {
			return
		}
		rel := strings.TrimPrefix(entry.Path, "/")
		for _, pattern := range patterns {
			if ok, _ := doublestar.Match(pattern, rel); ok {
				seen[entry.ContentHash] = true
				if !cache.Exists(entry.ContentHash) {
					out = append(out, entry)
				}
				return
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

var ErrInvalidID = errors.New("invalid mount workspace id")

func IsValidID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func ValidateID(id string) error {
	if !IsValidID(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

func StateDir(mountsRoot, id string) string {
	if !IsValidID(id) {
		return filepath.Join(mountsRoot, "_invalid_mount_id")
	}
	return filepath.Join(mountsRoot, id)
}

func GsDir(mountsRoot, id string) string {
	return filepath.Join(StateDir(mountsRoot, id), "gs")
}

func NewID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func ReadRecord(mountsRoot, id string) (Record, error) {
	var rec Record
	if err := ValidateID(id); err != nil {
		return rec, err
	}
	raw, err := os.ReadFile(filepath.Join(StateDir(mountsRoot, id), "mount.json"))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, err
	}
	if rec.ID != id {
		return Record{}, fmt.Errorf("%w: record id %q does not match %q", ErrInvalidID, rec.ID, id)
	}
	return rec, nil
}

func WriteRecord(mountsRoot string, rec Record) error {
	if err := ValidateID(rec.ID); err != nil {
		return err
	}
	stateDir := StateDir(mountsRoot, rec.ID)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(stateDir, "mount.json")
	tmp := path + ".tmp"
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Records(mountsRoot string) ([]Record, error) {
	entries, err := os.ReadDir(mountsRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rec, err := ReadRecord(mountsRoot, entry.Name())
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPath < out[j].MountPath })
	return out, nil
}

func FindRecord(mountsRoot, cwd, selector string) (Record, error) {
	records, err := Records(mountsRoot)
	if err != nil {
		return Record{}, err
	}
	selector = strings.TrimSpace(selector)
	abs := selector
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	abs = filepath.Clean(abs)
	var best Record
	for _, rec := range records {
		if rec.ID == selector {
			return rec, nil
		}
		if abs == rec.MountPath || strings.HasPrefix(abs, rec.MountPath+string(filepath.Separator)) {
			if len(rec.MountPath) > len(best.MountPath) {
				best = rec
			}
		}
	}
	if best.ID == "" {
		return Record{}, NewError("not_a_lazy_workspace", "no lazy workspace at "+abs, "Run gs mount list to see lazy workspaces, or gs mount <slice> <dir> to make one.")
	}
	return best, nil
}

func ManifestPath(mountsRoot, id, commitID string) string {
	key := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' {
			return c
		}
		return '_'
	}, commitID)
	return filepath.Join(StateDir(mountsRoot, id), "manifest-"+key)
}

func LoadManifest(mountsRoot string, rec Record) (*lazyfs.Manifest, error) {
	m, err := ReadManifest(ManifestPath(mountsRoot, rec.ID, rec.BaseCommit), rec.BaseCommit)
	if errors.Is(err, fs.ErrNotExist) {
		return ReadManifest(filepath.Join(StateDir(mountsRoot, rec.ID), "manifest"), rec.BaseCommit)
	}
	return m, err
}

func RemoveStaleManifests(mountsRoot, id string, keep ...string) {
	want := map[string]bool{}
	for _, commit := range keep {
		want[filepath.Base(ManifestPath(mountsRoot, id, commit))] = true
	}
	entries, err := os.ReadDir(StateDir(mountsRoot, id))
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if (name == "manifest" || strings.HasPrefix(name, "manifest-")) && !want[name] {
			_ = os.Remove(filepath.Join(StateDir(mountsRoot, id), name))
		}
	}
}

func ManifestFileCount(manifest *lazyfs.Manifest) int {
	files := 0
	manifest.Files(func(lazyfs.BaseEntry) { files++ })
	return files
}

func WriteManifest(path string, entries []lazyfs.BaseEntry) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(file)
	enc := json.NewEncoder(w)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			file.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ReadManifest(path, commitID string) (*lazyfs.Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []lazyfs.BaseEntry
	dec := json.NewDecoder(bufio.NewReader(file))
	for dec.More() {
		var entry lazyfs.BaseEntry
		if err := dec.Decode(&entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return lazyfs.NewManifest(commitID, entries)
}

func ManifestEntries(listing []*corev1.TreeEntry) []lazyfs.BaseEntry {
	out := make([]lazyfs.BaseEntry, 0, len(listing))
	for _, entry := range listing {
		base := lazyfs.BaseEntry{Path: entry.GetPath(), Mode: entry.GetMode(), Size: entry.GetSize(), ContentHash: entry.GetContentHash(), Target: entry.GetSymlinkTarget()}
		switch entry.GetKind() {
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			base.Kind = lazyfs.KindDir
		case corev1.EntryKind_ENTRY_KIND_SYMLINK:
			base.Kind = lazyfs.KindSymlink
		default:
			if entry.GetMode()&0o170000 == 0o120000 {
				base.Kind = lazyfs.KindSymlink
			} else {
				base.Kind = lazyfs.KindFile
			}
		}
		out = append(out, base)
	}
	return out
}

func LazyWorkspaceID(root string) string {
	var marker struct {
		ID string `json:"id"`
	}
	raw, err := os.ReadFile(filepath.Join(root, ".gs", "lazy.json"))
	if err != nil {
		return ""
	}
	if json.Unmarshal(raw, &marker) != nil || !IsValidID(marker.ID) {
		return ""
	}
	return marker.ID
}

func MultisetFileHash(p, contentHash string, mode uint32) (uint64, uint64) {
	var modeBuf [16]byte
	modeBytes := strconv.AppendUint(modeBuf[:0], uint64(mode), 8)
	h1 := fnv.New64a()
	_, _ = h1.Write([]byte(p))
	_, _ = h1.Write([]byte{0})
	_, _ = h1.Write([]byte(contentHash))
	_, _ = h1.Write([]byte{0})
	_, _ = h1.Write(modeBytes)
	s1 := h1.Sum64()

	h2 := fnv.New64()
	_, _ = h2.Write([]byte("gs-manifest-v1:"))
	_, _ = h2.Write(modeBytes)
	_, _ = h2.Write([]byte{0})
	_, _ = h2.Write([]byte(p))
	_, _ = h2.Write([]byte{0})
	_, _ = h2.Write([]byte(contentHash))
	s2 := h2.Sum64()
	return s1, s2
}

func ManifestDigest(manifest *lazyfs.Manifest) string {
	if manifest == nil {
		return ""
	}
	var count int
	var sum1, sum2, xor1 uint64
	manifest.Files(func(entry lazyfs.BaseEntry) {
		count++
		s1, s2 := MultisetFileHash(entry.Path, entry.ContentHash, entry.Mode)
		sum1 += s1
		sum2 += s2
		xor1 ^= s1
	})
	return fmt.Sprintf("%d:%016x:%016x:%016x", count, sum1, sum2, xor1)
}
