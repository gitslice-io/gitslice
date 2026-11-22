package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// The listing cache keeps the listing a lazy workspace was made from, per
// slice definition and commit, under the client cache. Each slice keeps its
// newest listingsKeptPerSlice entries.
const listingsKeptPerSlice = 16

func listingCachePath(cache *clientcache.ObjectCache, slice *corev1.Slice, commitID string) (string, bool) {
	if cache == nil || slice == nil || slice.GetId() == "" || commitID == "" {
		return "", false
	}
	key := slice.GetDefinitionHash() + "@" + strings.TrimPrefix(commitID, "sha256:")
	for _, part := range []string{slice.GetId(), key} {
		if strings.ContainsAny(part, `/\`) || strings.Contains(part, "..") {
			return "", false
		}
	}
	return filepath.Join(cache.Root(), "listings", slice.GetId(), key+".json"), true
}

func ReadCachedListing(cache *clientcache.ObjectCache, slice *corev1.Slice, commitID string) ([]*corev1.TreeEntry, bool) {
	if os.Getenv("GS_LISTING_CACHE") == "off" {
		return nil, false
	}
	path, ok := listingCachePath(cache, slice, commitID)
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var listing corev1.CheckoutEntries
	if protojson.Unmarshal(data, &listing) != nil {
		return nil, false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return listing.GetEntries(), true
}

// WriteCachedListing keeps a listing; failing to keep it is not an error.
func WriteCachedListing(cache *clientcache.ObjectCache, slice *corev1.Slice, commitID string, entries []*corev1.TreeEntry) {
	if os.Getenv("GS_LISTING_CACHE") == "off" {
		return
	}
	path, ok := listingCachePath(cache, slice, commitID)
	if !ok {
		return
	}
	data, err := protojson.Marshal(&corev1.CheckoutEntries{Entries: entries})
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "listing-*")
	if err != nil {
		return
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return
	}
	if tmp.Close() != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	pruneListingCache(dir)
}

func pruneListingCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= listingsKeptPerSlice {
		return
	}
	type aged struct {
		name string
		mod  int64
	}
	files := make([]aged, 0, len(entries))
	for _, e := range entries {
		if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, aged{e.Name(), info.ModTime().UnixNano()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod > files[j].mod })
	for _, f := range files[min(len(files), listingsKeptPerSlice):] {
		_ = os.Remove(filepath.Join(dir, f.name))
	}
}

// ListingBase is a cached listing a Checkout can ask for changes since.
type ListingBase struct {
	CommitID       string
	DefinitionHash string
	Entries        []*corev1.TreeEntry
}

func listingRefPath(cache *clientcache.ObjectCache, ref *corev1.SliceRef) (string, bool) {
	if cache == nil || ref == nil || ref.GetAccount() == "" || ref.GetSlice() == "" {
		return "", false
	}
	name := ref.GetAccount() + "@" + ref.GetSlice()
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", false
	}
	return filepath.Join(cache.Root(), "listings", "refs", name), true
}

// RememberListingRef records the slice id ref resolved to.
func RememberListingRef(cache *clientcache.ObjectCache, ref *corev1.SliceRef, slice *corev1.Slice) {
	path, ok := listingRefPath(cache, ref)
	if !ok || slice.GetId() == "" || os.Getenv("GS_LISTING_CACHE") == "off" {
		return
	}
	if current, err := os.ReadFile(path); err == nil && string(current) == slice.GetId() {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "ref-*")
	if err != nil {
		return
	}
	_, werr := tmp.WriteString(slice.GetId())
	if tmp.Close() != nil || werr != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// NewestCachedListing returns the most recently used listing the cache has
// for the slice ref names.
func NewestCachedListing(cache *clientcache.ObjectCache, ref *corev1.SliceRef) (*ListingBase, bool) {
	if os.Getenv("GS_LISTING_CACHE") == "off" {
		return nil, false
	}
	refPath, ok := listingRefPath(cache, ref)
	if !ok {
		return nil, false
	}
	sliceID, err := os.ReadFile(refPath)
	if err != nil || len(sliceID) == 0 || strings.ContainsAny(string(sliceID), `/\`) || strings.Contains(string(sliceID), "..") {
		return nil, false
	}
	dir := filepath.Join(cache.Root(), "listings", string(sliceID))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(newestMod) {
			newest, newestMod = e.Name(), info.ModTime()
		}
	}
	key := strings.TrimSuffix(newest, ".json")
	at := strings.LastIndex(key, "@")
	if at <= 0 || at == len(key)-1 {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(dir, newest))
	if err != nil {
		return nil, false
	}
	var listing corev1.CheckoutEntries
	if protojson.Unmarshal(data, &listing) != nil {
		return nil, false
	}
	commitID := key[at+1:]
	if !strings.Contains(commitID, ":") {
		commitID = "sha256:" + commitID
	}
	return &ListingBase{CommitID: commitID, DefinitionHash: key[:at], Entries: listing.GetEntries()}, true
}

// ApplyListingDelta applies a Checkout delta to the listing it was made
// against: removed paths go with everything under them, then the changed
// entries are added or replace what was there. The result is in path order.
func ApplyListingDelta(base []*corev1.TreeEntry, removed []string, changed []*corev1.TreeEntry) []*corev1.TreeEntry {
	gone := make(map[string]bool, len(removed))
	for _, p := range removed {
		gone[p] = true
	}
	byPath := make(map[string]*corev1.TreeEntry, len(base)+len(changed))
	for _, entry := range base {
		if len(gone) > 0 && removedByDelta(entry.GetPath(), gone) {
			continue
		}
		byPath[entry.GetPath()] = entry
	}
	for _, entry := range changed {
		byPath[entry.GetPath()] = entry
	}
	out := make([]*corev1.TreeEntry, 0, len(byPath))
	for _, entry := range byPath {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetPath() < out[j].GetPath() })
	return out
}

func removedByDelta(p string, gone map[string]bool) bool {
	for {
		if gone[p] {
			return true
		}
		i := strings.LastIndex(p, "/")
		if i <= 0 {
			return false
		}
		p = p[:i]
	}
}
