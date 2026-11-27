package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
)

func TestCachedPrincipalResolver(t *testing.T) {
	at := time.Unix(1_000_000, 0)
	now := func() time.Time { return at }
	calls := map[string]int{}
	resolve := cachedPrincipalResolver(func(_ context.Context, token string) (*authctx.Principal, error) {
		calls[token]++
		if token == "bad" {
			return nil, errors.New("unauthenticated")
		}
		return &authctx.Principal{SubjectID: "subject-" + token}, nil
	}, now)
	ctx := context.Background()
	jwt := func(sub string, exp time.Time) string {
		payload := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"sub":%q,"exp":%d}`, sub, exp.Unix()))
		return "header." + payload + ".signature"
	}

	// A signed token is remembered within the TTL.
	long := jwt("x", at.Add(time.Hour))
	for range 3 {
		if got, err := resolve(ctx, long); err != nil || got.SubjectID != "subject-"+long {
			t.Fatalf("resolve = %+v, %v", got, err)
		}
	}
	if calls[long] != 1 {
		t.Fatalf("a JWT resolved %d times within the TTL, want 1", calls[long])
	}
	// API keys and sessions are checked every time, so revoking one is
	// immediate.
	_, _ = resolve(ctx, "gsk_key")
	_, _ = resolve(ctx, "gsk_key")
	if calls["gsk_key"] != 2 {
		t.Fatalf("an API key resolved %d times, want 2", calls["gsk_key"])
	}
	// Failures are not remembered.
	_, _ = resolve(ctx, "bad")
	_, _ = resolve(ctx, "bad")
	if calls["bad"] != 2 {
		t.Fatalf("bad resolved %d times, want 2", calls["bad"])
	}
	// After the TTL it is resolved again.
	at = at.Add(subjectCacheTTL)
	_, _ = resolve(ctx, long)
	if calls[long] != 2 {
		t.Fatalf("a JWT resolved %d times after the TTL, want 2", calls[long])
	}

	// A JWT is remembered only until it expires.
	short := jwt("short", at.Add(5*time.Second))
	_, _ = resolve(ctx, short)
	at = at.Add(6 * time.Second)
	_, _ = resolve(ctx, short)
	if calls[short] != 2 {
		t.Fatalf("an expired JWT was served from the cache (%d resolves)", calls[short])
	}
	expired := jwt("exp", at.Add(-time.Minute))
	_, _ = resolve(ctx, expired)
	_, _ = resolve(ctx, expired)
	if calls[expired] != 2 {
		t.Fatalf("a JWT past its expiry was cached (%d resolves)", calls[expired])
	}
}

func TestCachedPrincipalResolverSingleflightAndLRUEviction(t *testing.T) {
	at := time.Unix(1_000_000, 0)
	now := func() time.Time { return at }

	var calls atomic.Int32
	gate := make(chan struct{})
	var openOnce sync.Once
	var mu sync.Mutex
	perTokenCalls := map[string]int{}

	resolve := newCachedPrincipalResolver(func(_ context.Context, token string) (*authctx.Principal, error) {
		calls.Add(1)
		<-gate
		mu.Lock()
		perTokenCalls[token]++
		mu.Unlock()
		return &authctx.Principal{
			SubjectID: "subject-" + token,
			SliceIDs:  []string{"sl_1"},
		}, nil
	}, now, 2)

	jwt := func(sub string) string {
		payload := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"sub":%q,"exp":%d}`, sub, at.Add(time.Hour).Unix()))
		return "header." + payload + ".signature"
	}

	tokA := jwt("a")
	const concurrency = 12
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := resolve(context.Background(), tokA)
			if err != nil || got.SubjectID != "subject-"+tokA {
				t.Errorf("resolve(tokA) = %+v, %v", got, err)
				return
			}
			// Mutating the returned slice must not corrupt cached entries.
			got.SliceIDs[0] = "mutated"
		}()
	}
	time.Sleep(20 * time.Millisecond)
	openOnce.Do(func() { close(gate) })
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent cold resolves = %d, want 1", got)
	}
	gotA, err := resolve(context.Background(), tokA)
	if err != nil || len(gotA.SliceIDs) != 1 || gotA.SliceIDs[0] != "sl_1" {
		t.Fatalf("cached principal was mutated across callers: %+v, %v", gotA, err)
	}

	// Capacity is 2: insert tokB, touch tokA (making tokB LRU), then insert tokC.
	// tokA must remain cached (no clear-all stampede!) while tokB is evicted.
	tokB := jwt("b")
	tokC := jwt("c")
	if _, err := resolve(context.Background(), tokB); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(context.Background(), tokA); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(context.Background(), tokC); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(context.Background(), tokA); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	aCalls := perTokenCalls[tokA]
	mu.Unlock()
	if aCalls != 1 {
		t.Fatalf("MRU token tokA was evicted on capacity overflow (calls = %d, want 1)", aCalls)
	}

	if _, err := resolve(context.Background(), tokB); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	bCalls := perTokenCalls[tokB]
	mu.Unlock()
	if bCalls != 2 {
		t.Fatalf("LRU token tokB calls = %d, want 2 after eviction", bCalls)
	}
}
