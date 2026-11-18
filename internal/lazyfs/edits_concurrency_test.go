package lazyfs

import (
	"context"
	"testing"
	"time"
)

type blockingHashContent struct {
	Content
	started chan struct{}
	release chan struct{}
}

func (b *blockingHashContent) HashFile(path string) (string, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
	return b.Content.HashFile(path)
}

func TestEditsHashesOutsideLockAllowingConcurrentOps(t *testing.T) {
	store := newMemContent()
	base := newBase(t, store, "c1", map[string]baseFile{
		"/app/existing.go": {content: "package app\n"},
	})
	blocking := &blockingHashContent{
		Content: store,
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	f, err := Open(t.TempDir(), base, blocking, Options{NoSync: true})
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.WriteFile("/app/large.bin", []byte("modified payload"), ModeFile); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}

	type editsRes struct {
		edits []Edit
		err   error
	}
	editsDone := make(chan editsRes, 1)
	go func() {
		edits, err := f.Edits()
		editsDone <- editsRes{edits: edits, err: err}
	}()

	select {
	case <-blocking.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Edits() to enter HashFile")
	}

	// While HashFile is blocked, concurrent Stat, ReadDir, Open, and WriteFile
	// must NOT block on f.mu!
	opsDone := make(chan error, 1)
	go func() {
		if _, err := f.Stat("/app/existing.go"); err != nil {
			opsDone <- err
			return
		}
		if _, err := f.ReadDir("/app"); err != nil {
			opsDone <- err
			return
		}
		if _, err := f.ReadFile(context.Background(), "/app/existing.go"); err != nil {
			opsDone <- err
			return
		}
		if err := f.WriteFile("/app/concurrent.txt", []byte("hello"), ModeFile); err != nil {
			opsDone <- err
			return
		}
		opsDone <- nil
	}()

	select {
	case err := <-opsDone:
		if err != nil {
			close(blocking.release)
			t.Fatalf("concurrent FS op failed while HashFile was running: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(blocking.release)
		t.Fatal("concurrent FS operations stalled while Edits() was hashing")
	}

	close(blocking.release)
	select {
	case res := <-editsDone:
		if res.err != nil {
			t.Fatalf("Edits() = %v", res.err)
		}
		if len(res.edits) != 1 || res.edits[0].Path != "/app/large.bin" {
			t.Fatalf("Edits() = %+v, want [/app/large.bin]", res.edits)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Edits() to finish")
	}
}
