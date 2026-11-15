package cli

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
// slice definition and commit, under the client cache. A commit's listing
// never changes, and the server still checks access (it sends the header
// only to a caller who may read the slice) before a cached listing is used,
// so a second workspace on the same commit, or one made again, skips the
// listing transfer. Each slice keeps its newest listingsKeptPerSlice.
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

func readCachedListing(cache *clientcache.ObjectCache, slice *corev1.Slice, commitID string) ([]*corev1.TreeEntry, bool) {
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

// writeCachedListing keeps a listing; failing to keep it is not an error.
func writeCachedListing(cache *clientcache.ObjectCache, slice *corev1.Slice, commitID string, entries []*corev1.TreeEntry) {
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
