//go:build linux || darwin

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

// The mount supervisor (gs mountd, started by gs mount) is the one background
// process that serves all of a user's lazy workspaces: each is still its own
// kernel mount with its own layers and fetcher (attachedWorkspace), and its
// own control socket, so every command that talks to a workspace's mount is
// unchanged. The supervisor adds one socket of its own, to attach workspaces.
// It exits a minute after its last workspace is unmounted.

// defaultMountdIdle is how long the supervisor stays up with nothing mounted.
const defaultMountdIdle = time.Minute

func mountdIdleTimeout() time.Duration {
	if value := os.Getenv("GS_MOUNTD_IDLE"); value != "" {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	return defaultMountdIdle
}

type mountSupervisor struct {
	r      Runner
	ctx    context.Context
	logger *log.Logger

	mu         sync.Mutex
	workspaces map[string]*attachedWorkspace
	attaching  map[string]bool
	idleSince  time.Time
	done       sync.WaitGroup // one per attached workspace, until it is unmounted
}

// runMountSupervisor serves until ctx ends, a signal arrives, or it has been
// idle for the idle timeout.
func (r Runner) runMountSupervisor(ctx context.Context) error {
	socket := r.mountSupervisorSocket()
	if _, err := callMountd(socket, mountdRequest{Op: "ping"}); err == nil {
		return nil // one is already running
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("mount supervisor cannot listen on %s: %w", socket, err)
	}
	defer os.Remove(socket)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s := &mountSupervisor{
		r:          r,
		ctx:        ctx,
		logger:     log.New(os.Stderr, "mountd: ", log.LstdFlags),
		workspaces: map[string]*attachedWorkspace{},
		attaching:  map[string]bool{},
		idleSince:  time.Now(),
	}
	s.logger.Printf("started (pid %d), serving %s", os.Getpid(), socket)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()

	idle := mountdIdleTimeout()
	tick := time.NewTicker(min(idle/4, time.Second))
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
			s.mu.Lock()
			quiet := len(s.workspaces) == 0 && len(s.attaching) == 0 && time.Since(s.idleSince) >= idle
			s.mu.Unlock()
			if quiet {
				s.logger.Printf("nothing mounted for %s, exiting", idle)
				break loop
			}
		}
	}
	_ = listener.Close()
	s.unmountAll()
	return nil
}

func (s *mountSupervisor) logf(format string, args ...any) { s.logger.Printf(format, args...) }

func (s *mountSupervisor) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Minute))
	var req mountdRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(conn).Encode(s.handle(req))
}

func (s *mountSupervisor) handle(req mountdRequest) (reply mountdReply) {
	defer func() {
		if p := recover(); p != nil {
			s.logger.Printf("panic handling %s: %v", req.Op, p)
			reply = mountdReply{PID: os.Getpid(), Error: fmt.Sprintf("internal error: %v", p)}
		}
	}()
	switch req.Op {
	case "ping":
		return mountdReply{PID: os.Getpid()}
	case "list":
		s.mu.Lock()
		defer s.mu.Unlock()
		ids := make([]string, 0, len(s.workspaces))
		for id := range s.workspaces {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return mountdReply{PID: os.Getpid(), Workspaces: ids}
	case "attach":
		return s.attach(req.ID)
	}
	return mountdReply{PID: os.Getpid(), Error: "unknown request " + req.Op}
}

func mountdError(err error) mountdReply {
	reply := mountdReply{PID: os.Getpid(), Error: err.Error()}
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		reply.ErrorCode, reply.Error, reply.Hint = cmdErr.Code, cmdErr.Message, cmdErr.Hint
	}
	return reply
}

// attach mounts the workspace the record names and serves it until it is
// unmounted.
func (s *mountSupervisor) attach(id string) mountdReply {
	s.mu.Lock()
	if s.workspaces[id] != nil || s.attaching[id] {
		s.mu.Unlock()
		return mountdReply{PID: os.Getpid(), ErrorCode: "already_mounted", Error: "lazy workspace " + id + " is already mounted", Hint: "Run gs mount list, or gs unmount it first."}
	}
	s.attaching[id] = true
	s.mu.Unlock()
	finish := func() {
		s.mu.Lock()
		delete(s.attaching, id)
		s.mu.Unlock()
	}

	rec, err := s.r.readMountRecord(id)
	if err != nil {
		finish()
		return mountdError(userError("not_a_lazy_workspace", "no lazy workspace "+id, "Run gs mount list."))
	}
	listener, closeListener := listenMountControl(id)
	w, err := s.r.attachWorkspace(s.ctx, rec, false, s.logf)
	if err != nil {
		closeListener()
		finish()
		s.logger.Printf("cannot mount %s at %s: %v", id, rec.MountPath, err)
		return mountdError(err)
	}
	s.mu.Lock()
	s.workspaces[id] = w
	delete(s.attaching, id)
	s.done.Add(1)
	s.mu.Unlock()
	if listener != nil {
		go serveMountControl(listener, w.handle)
	}
	s.logger.Printf("mounted %s:%s (%s) at %s", rec.Account, rec.Slice, id, rec.MountPath)
	go func() {
		defer s.done.Done()
		w.wait()
		closeListener()
		w.close()
		s.mu.Lock()
		delete(s.workspaces, id)
		if len(s.workspaces) == 0 {
			s.idleSince = time.Now()
		}
		s.mu.Unlock()
		s.logger.Printf("unmounted %s from %s", id, rec.MountPath)
	}()
	return mountdReply{PID: os.Getpid()}
}

// unmountAll unmounts every workspace, waiting a little for each to finish.
func (s *mountSupervisor) unmountAll() {
	s.mu.Lock()
	all := make([]*attachedWorkspace, 0, len(s.workspaces))
	for _, w := range s.workspaces {
		all = append(all, w)
	}
	s.mu.Unlock()
	for _, w := range all {
		w.unmount()
	}
	finished := make(chan struct{})
	go func() {
		s.done.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		s.logger.Printf("some workspaces were still busy when the supervisor stopped")
	}
}
