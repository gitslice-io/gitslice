//go:build linux || darwin

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// listenMountControl listens on a workspace's control socket. It is made before
// the mount appears, so a command run right after gs mount always finds it
// (connections wait until served). The returned func closes and removes it. A
// workspace still works without one, only its commands cannot reach it.
func listenMountControl(id string) (net.Listener, func()) {
	socket := mountControlPath(id)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, func() {}
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, func() {}
	}
	return listener, func() {
		_ = listener.Close()
		_ = os.Remove(socket)
	}
}

// serveMount serves one lazy workspace at its mount path, in this process,
// until it is unmounted, asked to over the control socket, interrupted, or ctx
// ends: gs mount --foreground. gs mount without it hands the workspace to the
// mount supervisor instead.
func (r Runner) serveMount(ctx context.Context, rec mountRecord, debug bool, ready func()) error {
	listener, closeListener := listenMountControl(rec.ID)
	defer closeListener()
	w, err := r.attachWorkspace(ctx, rec, debug, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})
	if err != nil {
		return err
	}
	defer w.close()
	if listener != nil {
		go serveMountControl(listener, w.handle)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-signals:
			w.unmount()
		case <-ctx.Done():
			w.unmount()
		case <-stop:
		}
	}()
	if ready != nil {
		ready()
	}
	w.wait()
	return nil
}

func serveMountControl(listener net.Listener, handle func(mountControlRequest) mountControlReply) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
			var req mountControlRequest
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				return
			}
			_ = json.NewEncoder(conn).Encode(handle(req))
		}(conn)
	}
}

// attachViaSupervisor has the mount supervisor mount a workspace, starting the
// supervisor if none is running, and returns once the mount is up.
func (r Runner) attachViaSupervisor(rec mountRecord) error {
	socket := r.mountSupervisorSocket()
	reply, err := callMountd(socket, mountdRequest{Op: "attach", ID: rec.ID})
	if errors.Is(err, errMountUnreachable) {
		if err := r.startMountSupervisor(socket); err != nil {
			return err
		}
		reply, err = callMountd(socket, mountdRequest{Op: "attach", ID: rec.ID})
	}
	_ = reply
	if err != nil {
		return err
	}
	if !isMounted(rec.MountPath) {
		return userError("mount_failed", "the mount supervisor attached "+rec.MountPath+" but it is not mounted", "See "+r.mountSupervisorLog()+".")
	}
	return nil
}

func (r Runner) mountSupervisorLog() string { return filepath.Join(r.mountsRoot(), "mountd.log") }

// startMountSupervisor starts the supervisor as a detached copy of gs and waits
// until it answers on its socket.
func (r Runner) startMountSupervisor(socket string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := r.mountSupervisorLog()
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
	if r.Home != "" {
		cmd.Env = append(cmd.Env, "HOME="+r.Home)
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
		if _, err := callMountd(socket, mountdRequest{Op: "ping"}); err == nil {
			_ = cmd.Process.Release()
			return nil
		}
		select {
		case err := <-exited:
			return userError("mount_failed", fmt.Sprintf("the mount supervisor exited (%v): %s", err, daemonLogTail(logPath, 5)), "See "+logPath+".")
		case <-deadline:
			return userError("mount_failed", "the mount supervisor did not start within 15 s: "+daemonLogTail(logPath, 5), "See "+logPath+".")
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

// forceUnmount unmounts a mount whose daemon is gone.
func forceUnmount(dir string) error {
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
