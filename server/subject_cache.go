package server

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// A client making many calls in a row (the web app, a service account) would
// otherwise pay a signature check, and a subject lookup or upsert, on every
// call. cachedSubjectResolver remembers a resolved signed token (a JWT: a
// Clerk session token or a service token) for subjectCacheTTL, never past its
// own expiry. Such tokens are valid by signature and expiry alone, so caching
// them extends nothing. Credentials checked against the database (API keys,
// sessions) are not cached: revoking one takes effect on the next call.
// Failures are not cached.
const (
	subjectCacheTTL           = 30 * time.Second
	subjectCacheMaxEntries    = 10000
	subjectResolveCallTimeout = 10 * time.Second
)

type subjectCacheEntry struct {
	key       [sha256.Size]byte
	subjectID string
	expires   time.Time
}

func cachedSubjectResolver(resolve subjectResolver, now func() time.Time) subjectResolver {
	return newCachedSubjectResolver(resolve, now, subjectCacheMaxEntries)
}

func newCachedSubjectResolver(resolve subjectResolver, now func() time.Time, maxEntries int) subjectResolver {
	if maxEntries <= 0 {
		maxEntries = subjectCacheMaxEntries
	}
	var (
		mu      sync.Mutex
		entries = make(map[[sha256.Size]byte]*list.Element)
		lru     = list.New()
		sf      singleflight.Group
	)

	lookup := func(key [sha256.Size]byte, at time.Time) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		elem, ok := entries[key]
		if !ok {
			return "", false
		}
		entry := elem.Value.(*subjectCacheEntry)
		if !at.Before(entry.expires) {
			lru.Remove(elem)
			delete(entries, key)
			return "", false
		}
		lru.MoveToFront(elem)
		return entry.subjectID, true
	}

	store := func(key [sha256.Size]byte, subjectID string, at, expires time.Time) {
		mu.Lock()
		defer mu.Unlock()
		if elem, ok := entries[key]; ok {
			entry := elem.Value.(*subjectCacheEntry)
			entry.subjectID = subjectID
			entry.expires = expires
			lru.MoveToFront(elem)
			return
		}
		if len(entries) >= maxEntries {
			for k, elem := range entries {
				if !at.Before(elem.Value.(*subjectCacheEntry).expires) {
					lru.Remove(elem)
					delete(entries, k)
				}
			}
			for len(entries) >= maxEntries {
				oldest := lru.Back()
				if oldest == nil {
					break
				}
				evicted := oldest.Value.(*subjectCacheEntry)
				lru.Remove(oldest)
				delete(entries, evicted.key)
			}
		}
		entries[key] = lru.PushFront(&subjectCacheEntry{
			key:       key,
			subjectID: subjectID,
			expires:   expires,
		})
	}

	return func(ctx context.Context, token string) (string, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}

		key := sha256.Sum256([]byte(token))
		if subjectID, ok := lookup(key, now()); ok {
			return subjectID, nil
		}

		// Coalesce concurrent cold-cache lookups for the same token digest
		// without storing the raw bearer secret in the singleflight key map.
		sfKey := hex.EncodeToString(key[:])
		ch := sf.DoChan(sfKey, func() (any, error) {
			if subjectID, ok := lookup(key, now()); ok {
				return subjectID, nil
			}
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subjectResolveCallTimeout)
			defer cancel()

			subjectID, err := resolve(callCtx, token)
			if err != nil || subjectID == "" {
				return subjectID, err
			}
			exp, signed := jwtExpiry(token)
			if !signed {
				return subjectID, nil
			}
			at := now()
			expires := at.Add(subjectCacheTTL)
			if exp.Before(expires) {
				expires = exp
			}
			if at.Before(expires) {
				store(key, subjectID, at, expires)
			}
			return subjectID, nil
		})

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case res := <-ch:
			if res.Err != nil {
				return "", res.Err
			}
			subjectID, _ := res.Val.(string)
			return subjectID, nil
		}
	}
}

// jwtExpiry reads the exp claim of a token shaped like a JWT. The token has
// already been verified by the resolver; this only bounds the cache.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(claims.Exp), 0), true
}
