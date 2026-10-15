package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCachedSubjectResolver(t *testing.T) {
	at := time.Unix(1_000_000, 0)
	now := func() time.Time { return at }
	calls := map[string]int{}
	resolve := cachedSubjectResolver(func(_ context.Context, token string) (string, error) {
		calls[token]++
		if token == "bad" {
			return "", errors.New("unauthenticated")
		}
		return "subject-" + token, nil
	}, now)
	ctx := context.Background()
	jwt := func(exp time.Time) string {
		payload := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"sub":"x","exp":%d}`, exp.Unix()))
		return "header." + payload + ".signature"
	}

	// A signed token is remembered within the TTL.
	long := jwt(at.Add(time.Hour))
	for range 3 {
		if got, err := resolve(ctx, long); err != nil || got != "subject-"+long {
			t.Fatalf("resolve = %q, %v", got, err)
		}
	}
	if calls[long] != 1 {
		t.Fatalf("a JWT resolved %d times within the TTL, want 1", calls[long])
	}
	// API keys and sessions are checked every time, so revoking one is
	// immediate.
	resolve(ctx, "gsk_key")
	resolve(ctx, "gsk_key")
	if calls["gsk_key"] != 2 {
		t.Fatalf("an API key resolved %d times, want 2", calls["gsk_key"])
	}
	// Failures are not remembered.
	resolve(ctx, "bad")
	resolve(ctx, "bad")
	if calls["bad"] != 2 {
		t.Fatalf("bad resolved %d times, want 2", calls["bad"])
	}
	// After the TTL it is resolved again.
	at = at.Add(subjectCacheTTL)
	resolve(ctx, long)
	if calls[long] != 2 {
		t.Fatalf("a JWT resolved %d times after the TTL, want 2", calls[long])
	}

	// A JWT is remembered only until it expires.
	short := jwt(at.Add(5 * time.Second))
	resolve(ctx, short)
	at = at.Add(6 * time.Second)
	resolve(ctx, short)
	if calls[short] != 2 {
		t.Fatalf("an expired JWT was served from the cache (%d resolves)", calls[short])
	}
	expired := jwt(at.Add(-time.Minute))
	resolve(ctx, expired)
	resolve(ctx, expired)
	if calls[expired] != 2 {
		t.Fatalf("a JWT past its expiry was cached (%d resolves)", calls[expired])
	}
}
