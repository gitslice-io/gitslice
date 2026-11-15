package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestCheckMountDir(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, files ...string) string {
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	workspace := mk("checkout", ".gs/slice.json")
	damaged := mk("damaged", ".gs/state.json")

	for _, tc := range []struct {
		name string
		dir  string
		code string // "" for a directory that can be mounted
	}{
		{"missing directory", filepath.Join(root, "new"), ""},
		{"empty directory", mk("empty"), ""},
		{"not empty", mk("full", "keep.txt"), "mount_dir_not_empty"},
		{"inside a checkout", filepath.Join(workspace, "sub", "mnt"), "already_in_workspace"},
		{"directly inside a checkout", filepath.Join(workspace, "mnt"), "already_in_workspace"},
		{"inside a workspace that lost its binding", filepath.Join(damaged, "mnt"), "workspace_damaged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMountDir(tc.dir)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("checkMountDir = %v, want nil", err)
				}
				return
			}
			if !isUserErrorCode(err, tc.code) {
				t.Fatalf("checkMountDir = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestRetryUnavailable(t *testing.T) {
	pauses := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	unavailable := grpcstatus.Error(codes.Unavailable, "error reading from server: EOF")
	run := func(ctx context.Context, results ...error) (int, error) {
		calls := 0
		err := retryUnavailable(ctx, pauses, func() error {
			calls++
			if calls > len(results) {
				t.Fatalf("called %d times", calls)
			}
			return results[calls-1]
		})
		return calls, err
	}
	if calls, err := run(context.Background(), unavailable, unavailable, nil); calls != 3 || err != nil {
		t.Fatalf("recovers on the third try: calls = %d, err = %v", calls, err)
	}
	if calls, err := run(context.Background(), unavailable, unavailable, unavailable, unavailable); calls != 4 || grpcstatus.Code(err) != codes.Unavailable {
		t.Fatalf("gives up after the pauses: calls = %d, err = %v", calls, err)
	}
	notFound := grpcstatus.Error(codes.NotFound, "no such content")
	if calls, err := run(context.Background(), notFound); calls != 1 || !errors.Is(err, notFound) {
		t.Fatalf("other errors are not retried: calls = %d, err = %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if calls, _ := run(ctx, unavailable); calls != 1 {
		t.Fatalf("a cancelled context stops the retries: calls = %d", calls)
	}
}

// After a fetch gave up on an unreachable server, the next ones fail at once.
func TestMountFetcherFailsFastDuringAnOutage(t *testing.T) {
	cache, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &mountFetcher{cache: cache, inflight: map[string]*fetchCall{}, downUntil: time.Now().Add(time.Minute)}
	started := time.Now()
	_, err = f.fetchContent("/a.txt", "sha256:abc", 3)
	if !errors.Is(err, errServerUnreachable) {
		t.Fatalf("fetchContent during an outage = %v, want errServerUnreachable", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("fetchContent took %s during an outage; it should fail at once", time.Since(started))
	}
}
