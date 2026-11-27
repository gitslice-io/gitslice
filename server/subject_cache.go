package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/lru"
)

// A client making many calls in a row (the web app, a service account) would
// otherwise pay a signature check, and a subject lookup or upsert, on every
// call. cachedPrincipalResolver remembers a resolved signed token (a JWT: a
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

func cloneCachedPrincipal(p *authctx.Principal) *authctx.Principal {
	if p == nil {
		return nil
	}
	cp := *p
	cp.SliceIDs = slices.Clone(p.SliceIDs)
	return &cp
}

func cachedPrincipalResolver(resolve principalResolver, now func() time.Time) principalResolver {
	return newCachedPrincipalResolver(resolve, now, subjectCacheMaxEntries)
}

func newCachedPrincipalResolver(resolve principalResolver, now func() time.Time, maxEntries int) principalResolver {
	if maxEntries <= 0 {
		maxEntries = subjectCacheMaxEntries
	}
	if now == nil {
		now = time.Now
	}
	cache := lru.New[[sha256.Size]byte, *authctx.Principal](
		maxEntries,
		lru.WithNow[*authctx.Principal](now),
		lru.WithClone(cloneCachedPrincipal),
	)

	return func(ctx context.Context, token string) (*authctx.Principal, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Coalesce concurrent cold-cache lookups for the same token digest
		// without storing the raw bearer secret in the singleflight key map.
		key := sha256.Sum256([]byte(token))
		sfKey := hex.EncodeToString(key[:])
		return cache.GetOrLoad(ctx, key, sfKey, func(loadCtx context.Context) (*authctx.Principal, time.Time, bool, error) {
			callCtx, cancel := context.WithTimeout(loadCtx, subjectResolveCallTimeout)
			defer cancel()

			principal, err := resolve(callCtx, token)
			if err != nil || principal == nil || principal.SubjectID == "" {
				return principal, time.Time{}, false, err
			}
			exp, signed := jwtExpiry(token)
			if !signed {
				return principal, time.Time{}, false, nil
			}
			at := now()
			expires := at.Add(subjectCacheTTL)
			if exp.Before(expires) {
				expires = exp
			}
			if !at.Before(expires) {
				return principal, time.Time{}, false, nil
			}
			return principal, expires, true, nil
		})
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
