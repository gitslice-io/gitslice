package fusefs

import (
	"errors"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

func TestMountWithinGivesUpOnAMountThatNeverReturns(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	server, err := mountWithin(50*time.Millisecond, func() (*fuse.Server, error) {
		<-release // a driver waiting to be approved
		return nil, nil
	})
	if !errors.Is(err, ErrMountTimeout) || server != nil {
		t.Fatalf("mountWithin = %v, %v; want ErrMountTimeout", server, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %s", took)
	}
}

func TestMountWithinPassesAnErrorAndAnEarlyResultThrough(t *testing.T) {
	failure := errors.New("fusermount: permission denied")
	if _, err := mountWithin(time.Second, func() (*fuse.Server, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the mount's own", err)
	}
	server := &fuse.Server{}
	got, err := mountWithin(time.Second, func() (*fuse.Server, error) { return server, nil })
	if err != nil || got != server {
		t.Fatalf("mountWithin = %v, %v", got, err)
	}
}

// A mount that comes up after the wait has no one serving it, so the late
// result is dropped instead of leaking: the goroutine that waits for it ends.
func TestMountWithinDrainsALateErrorWithoutLeaking(t *testing.T) {
	release := make(chan struct{})
	_, err := mountWithin(20*time.Millisecond, func() (*fuse.Server, error) {
		<-release
		return nil, errors.New("late failure")
	})
	if !errors.Is(err, ErrMountTimeout) {
		t.Fatalf("error = %v", err)
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
}
