//go:build linux || darwin

package mountd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultIdleTimeout = time.Minute

func idleTimeout() time.Duration {
	if value := os.Getenv("GS_MOUNTD_IDLE"); value != "" {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	return defaultIdleTimeout
}

// ListenMountControl listens on workspace id's control socket under runtimeDir.
func ListenMountControl(runtimeDir, id string) (net.Listener, func()) {
	if err := ValidateID(id); err != nil {
		return nil, func() {}
	}
	socket := ControlPath(runtimeDir, id)
	listener, err := ListenControlSocket(socket)
	if err != nil {
		return nil, func() {}
	}
	return listener, func() {
		_ = listener.Close()
		_ = os.Remove(socket)
	}
}

// ServeMountControl accepts peer-verified JSON control requests on listener.
func ServeMountControl(listener net.Listener, handle func(ControlRequest) ControlReply) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			if err := VerifyPeerUID(conn); err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
			var req ControlRequest
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				return
			}
			_ = json.NewEncoder(conn).Encode(handle(req))
		}(conn)
	}
}

// ServeMount serves one lazy workspace in the foreground until unmounted.
func (e Env) ServeMount(ctx context.Context, rec Record, debug bool, ready func()) error {
	listener, closeListener := ListenMountControl(e.RuntimeDir, rec.ID)
	defer closeListener()
	w, err := e.AttachWorkspace(ctx, rec, debug, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})
	if err != nil {
		return err
	}
	defer w.Close()
	if listener != nil {
		go ServeMountControl(listener, w.Handle)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-signals:
			w.Unmount()
		case <-ctx.Done():
			w.Unmount()
		case <-stop:
		}
	}()
	if ready != nil {
		ready()
	}
	w.Wait()
	return nil
}

// AttachViaSupervisor attaches rec through the background mount supervisor,
// starting it if none is running.
func (e Env) AttachViaSupervisor(rec Record) error {
	socket := SupervisorSocket(e.RuntimeDir, e.MountsRoot)
	_, err := CallSupervisor(socket, SupervisorRequest{Op: "attach", ID: rec.ID})
	if errors.Is(err, ErrUnreachable) {
		if err := e.startSupervisor(socket); err != nil {
			return err
		}
		_, err = CallSupervisor(socket, SupervisorRequest{Op: "attach", ID: rec.ID})
	}
	if err != nil {
		return err
	}
	if !IsMounted(rec.MountPath) {
		return NewError("mount_failed", "the mount supervisor attached "+rec.MountPath+" but it is not mounted", "See "+e.supervisorLog()+".")
	}
	return nil
}

func (e Env) supervisorLog() string { return filepath.Join(e.MountsRoot, "mountd.log") }

func (e Env) startSupervisor(socket string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := e.supervisorLog()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "mountd")
	cmd.Env = os.Environ()
	if e.Home != "" {
		cmd.Env = append(cmd.Env, "HOME="+e.Home)
		cmd.Env = append(cmd.Env, "XDG_RUNTIME_DIR="+filepath.Dir(socket))
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(15 * time.Second)
	for {
		if _, err := CallSupervisor(socket, SupervisorRequest{Op: "ping"}); err == nil {
			_ = cmd.Process.Release()
			return nil
		}
		select {
		case err := <-exited:
			return NewError("mount_failed", fmt.Sprintf("the mount supervisor exited (%v): %s", err, daemonLogTail(logPath, 5)), "See "+logPath+".")
		case <-deadline:
			return NewError("mount_failed", "the mount supervisor did not start within 15 s: "+daemonLogTail(logPath, 5), "See "+logPath+".")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func daemonLogTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// ForceUnmount unmounts dir when its FUSE daemon is gone.
func ForceUnmount(dir string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		if err := exec.Command("umount", dir).Run(); err == nil {
			return nil
		}
		cmd = exec.Command("diskutil", "unmount", "force", dir)
	} else if path, err := exec.LookPath("fusermount3"); err == nil {
		cmd = exec.Command(path, "-u", dir)
	} else {
		cmd = exec.Command("fusermount", "-u", dir)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

// Supervisor serves multiple mounted lazy workspaces in one background process.
type Supervisor struct {
	env    Env
	ctx    context.Context
	logger *log.Logger

	mu         sync.Mutex
	workspaces map[string]*AttachedWorkspace
	attaching  map[string]bool
	idleSince  time.Time
	done       sync.WaitGroup
}

// NewSupervisor creates a Supervisor for unit tests or in-process runs.
func NewSupervisor(ctx context.Context, env Env, logOut io.Writer) *Supervisor {
	if logOut == nil {
		logOut = os.Stderr
	}
	return &Supervisor{
		env:        env,
		ctx:        ctx,
		logger:     log.New(logOut, "mountd: ", log.LstdFlags),
		workspaces: map[string]*AttachedWorkspace{},
		attaching:  map[string]bool{},
		idleSince:  time.Now(),
	}
}

// RunSupervisor runs the mount supervisor until ctx ends, a signal arrives, or
// it has been idle for idleTimeout.
func (e Env) RunSupervisor(ctx context.Context) error {
	socket := SupervisorSocket(e.RuntimeDir, e.MountsRoot)
	if _, err := CallSupervisor(socket, SupervisorRequest{Op: "ping"}); err == nil {
		return nil
	}
	listener, err := ListenControlSocket(socket)
	if err != nil {
		return fmt.Errorf("mount supervisor cannot listen on %s: %w", socket, err)
	}
	defer os.Remove(socket)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s := NewSupervisor(ctx, e, os.Stderr)
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

	idle := idleTimeout()
	tick := time.NewTicker(min(idle/4, time.Second))
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
			if s.env.PruneObjectCache != nil {
				s.env.PruneObjectCache()
			}
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

func (s *Supervisor) logf(format string, args ...any) { s.logger.Printf(format, args...) }

func (s *Supervisor) serve(conn net.Conn) {
	defer conn.Close()
	if err := VerifyPeerUID(conn); err != nil {
		s.logger.Printf("rejected control connection: %v", err)
		return
	}
	_ = conn.SetDeadline(time.Now().Add(time.Minute))
	var req SupervisorRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(conn).Encode(s.Handle(req))
}

func (s *Supervisor) Handle(req SupervisorRequest) (reply SupervisorReply) {
	defer func() {
		if p := recover(); p != nil {
			s.logger.Printf("panic handling %s: %v", req.Op, p)
			reply = SupervisorReply{PID: os.Getpid(), Error: fmt.Sprintf("internal error: %v", p)}
		}
	}()
	switch req.Op {
	case "ping":
		return SupervisorReply{PID: os.Getpid()}
	case "list":
		s.mu.Lock()
		defer s.mu.Unlock()
		ids := make([]string, 0, len(s.workspaces))
		for id := range s.workspaces {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return SupervisorReply{PID: os.Getpid(), Workspaces: ids}
	case "attach":
		return s.Attach(req.ID)
	}
	return SupervisorReply{PID: os.Getpid(), Error: "unknown request " + req.Op}
}

func supervisorError(err error) SupervisorReply {
	reply := SupervisorReply{PID: os.Getpid(), Error: err.Error()}
	var mErr *Error
	if errors.As(err, &mErr) {
		reply.ErrorCode, reply.Error, reply.Hint = mErr.Code, mErr.Message, mErr.Hint
	}
	return reply
}

// Attach mounts the workspace named by id and serves it until unmounted.
func (s *Supervisor) Attach(id string) SupervisorReply {
	if err := ValidateID(id); err != nil {
		return supervisorError(NewError("invalid_workspace_id", "invalid lazy workspace id", "Run gs mount list."))
	}
	s.mu.Lock()
	if s.workspaces[id] != nil || s.attaching[id] {
		s.mu.Unlock()
		return SupervisorReply{PID: os.Getpid(), ErrorCode: "already_mounted", Error: "lazy workspace " + id + " is already mounted", Hint: "Run gs mount list, or gs unmount it first."}
	}
	s.attaching[id] = true
	s.mu.Unlock()
	finish := func() {
		s.mu.Lock()
		delete(s.attaching, id)
		s.mu.Unlock()
	}

	rec, err := ReadRecord(s.env.MountsRoot, id)
	if err != nil {
		finish()
		return supervisorError(NewError("not_a_lazy_workspace", "no lazy workspace "+id, "Run gs mount list."))
	}
	listener, closeListener := ListenMountControl(s.env.RuntimeDir, id)
	w, err := s.env.AttachWorkspace(s.ctx, rec, false, s.logf)
	if err != nil {
		closeListener()
		finish()
		s.logger.Printf("cannot mount %s at %s: %v", id, rec.MountPath, err)
		return supervisorError(err)
	}
	s.mu.Lock()
	s.workspaces[id] = w
	delete(s.attaching, id)
	s.done.Add(1)
	s.mu.Unlock()
	if listener != nil {
		go ServeMountControl(listener, w.Handle)
	}
	s.logger.Printf("mounted %s:%s (%s) at %s", rec.Account, rec.Slice, id, rec.MountPath)
	go func() {
		defer s.done.Done()
		w.Wait()
		closeListener()
		w.Close()
		s.mu.Lock()
		delete(s.workspaces, id)
		if len(s.workspaces) == 0 {
			s.idleSince = time.Now()
		}
		s.mu.Unlock()
		s.logger.Printf("unmounted %s from %s", id, rec.MountPath)
	}()
	return SupervisorReply{PID: os.Getpid()}
}

func (s *Supervisor) unmountAll() {
	s.mu.Lock()
	all := make([]*AttachedWorkspace, 0, len(s.workspaces))
	for _, w := range s.workspaces {
		all = append(all, w)
	}
	s.mu.Unlock()
	for _, w := range all {
		w.Unmount()
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
