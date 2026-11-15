package server

import (
	"container/list"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/socialcard"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/singleflight"
)

// sliceResolver is the part of the slice service the card needs.
type sliceResolver interface {
	ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error)
}

const (
	socialImageMaxAge         = "public, max-age=86400"
	socialGenericMaxAge       = "public, max-age=3600"
	socialCardCacheMaxEntries = 256
)

var renderDefaultSocialCard = sync.OnceValues(func() ([]byte, error) {
	return socialcard.Render(socialcard.Card{})
})

type socialCardCacheKey struct {
	account     string
	slice       string
	description string
}

type socialCardCacheEntry struct {
	key  socialCardCacheKey
	data []byte
}

type socialCardCache struct {
	maxEntries int
	render     func(socialcard.Card) ([]byte, error)
	sf         singleflight.Group

	mu      sync.Mutex
	entries map[socialCardCacheKey]*list.Element
	lru     *list.List
}

func newSocialCardCache(maxEntries int, render func(socialcard.Card) ([]byte, error)) *socialCardCache {
	if maxEntries <= 0 {
		maxEntries = socialCardCacheMaxEntries
	}
	if render == nil {
		render = socialcard.Render
	}
	return &socialCardCache{
		maxEntries: maxEntries,
		render:     render,
		entries:    make(map[socialCardCacheKey]*list.Element),
		lru:        list.New(),
	}
}

func (c *socialCardCache) getOrRender(card socialcard.Card) ([]byte, error) {
	key := socialCardCacheKey{
		account:     card.Account,
		slice:       card.Slice,
		description: card.Description,
	}
	if data, ok := c.get(key); ok {
		return data, nil
	}
	sfKey := key.account + "\x00" + key.slice + "\x00" + key.description
	val, err, _ := c.sf.Do(sfKey, func() (any, error) {
		if data, ok := c.get(key); ok {
			return data, nil
		}
		data, err := c.render(card)
		if err != nil {
			return nil, err
		}
		c.put(key, data)
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return val.([]byte), nil
}

func (c *socialCardCache) get(key socialCardCacheKey) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(elem)
	return elem.Value.(*socialCardCacheEntry).data, true
}

func (c *socialCardCache) put(key socialCardCacheKey, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[key]; ok {
		elem.Value.(*socialCardCacheEntry).data = data
		c.lru.MoveToFront(elem)
		return
	}
	for len(c.entries) >= c.maxEntries {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		evicted := oldest.Value.(*socialCardCacheEntry)
		c.lru.Remove(oldest)
		delete(c.entries, evicted.key)
	}
	c.entries[key] = c.lru.PushFront(&socialCardCacheEntry{key: key, data: data})
}

// newSocialImageHandler serves the preview images shared links show:
//
//	GET /og/slices/{account}/{slice}.png  the slice's card
//	GET /og/default.png                   the generic Gitslice card
//
// It is mounted without the auth middleware, so it always asks as an
// anonymous reader: only a public slice gets its own card, and a private,
// archived or missing one gets the generic card, which tells nothing about it.
// The web app's Worker proxies these and caches them at the edge.
func newSocialImageHandler(slices sliceResolver) http.Handler {
	return newSocialImageHandlerWithRenderer(slices, renderDefaultSocialCard, socialcard.Render)
}

func newSocialImageHandlerWithRenderer(
	slices sliceResolver,
	renderDefault func() ([]byte, error),
	renderSlice func(socialcard.Card) ([]byte, error),
) http.Handler {
	cache := newSocialCardCache(socialCardCacheMaxEntries, renderSlice)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		card, maxAge := socialcard.Card{}, socialGenericMaxAge
		if account, slice, ok := socialImageSlice(r.URL.Path); ok {
			resolved, err := slices.ResolveSlice(r.Context(), &corev1.ResolveSliceRequest{
				Ref: &corev1.SliceRef{Account: account, Slice: slice},
			})
			if err == nil && resolved.GetDefinition().GetVisibility() == "public" && !resolved.GetArchived() {
				ref := resolved.GetRef()
				card = socialcard.Card{
					Account:     ref.GetAccount(),
					Slice:       ref.GetSlice(),
					Description: resolved.GetDescription(),
					Facts:       []string{"Public slice", "gitslice.io/slices/" + ref.GetAccount() + "/" + ref.GetSlice()},
				}
				maxAge = socialImageMaxAge
			}
		} else if r.URL.Path != "/og/default.png" {
			http.NotFound(w, r)
			return
		}

		// HEAD probes never need expensive glyph layout or PNG encoding.
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", maxAge)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			return
		}

		var (
			data []byte
			err  error
		)
		if card.Slice == "" {
			data, err = renderDefault()
		} else {
			data, err = cache.getOrRender(card)
		}
		if err != nil {
			slog.Error("render social card", "card", card.Account+"/"+card.Slice, "error", err)
			http.Error(w, "could not draw the card", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", maxAge)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	})
}

// socialImageSlice reads the account and slice from /og/slices/{account}/{slice}.png.
func socialImageSlice(p string) (string, string, bool) {
	rest, ok := strings.CutPrefix(p, "/og/slices/")
	if !ok {
		return "", "", false
	}
	account, file, ok := strings.Cut(rest, "/")
	slice, isPNG := strings.CutSuffix(file, ".png")
	if !ok || !isPNG || account == "" || slice == "" || strings.Contains(slice, "/") {
		return "", "", false
	}
	return account, slice, true
}
