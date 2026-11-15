package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"
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
	subjectCacheTTL        = 30 * time.Second
	subjectCacheMaxEntries = 10000
)

type subjectCacheEntry struct {
	subjectID string
	expires   time.Time
}

func cachedSubjectResolver(resolve subjectResolver, now func() time.Time) subjectResolver {
	var mu sync.Mutex
	entries := map[[sha256.Size]byte]subjectCacheEntry{}
	return func(ctx context.Context, token string) (string, error) {
		key := sha256.Sum256([]byte(token))
		mu.Lock()
		entry, ok := entries[key]
		mu.Unlock()
		if ok && now().Before(entry.expires) {
			return entry.subjectID, nil
		}
		subjectID, err := resolve(ctx, token)
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
		if !at.Before(expires) {
			return subjectID, nil
		}
		mu.Lock()
		defer mu.Unlock()
		if len(entries) >= subjectCacheMaxEntries {
			for k, e := range entries {
				if !at.Before(e.expires) {
					delete(entries, k)
				}
			}
			if len(entries) >= subjectCacheMaxEntries {
				clear(entries)
			}
		}
		entries[key] = subjectCacheEntry{subjectID: subjectID, expires: expires}
		return subjectID, nil
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
