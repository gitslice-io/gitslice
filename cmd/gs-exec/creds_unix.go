//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func configureChildProcess(cmd *exec.Cmd) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if os.Geteuid() == 0 {
		attr.Credential = &syscall.Credential{Uid: runnerUID, Gid: runnerGID}
	}
	cmd.SysProcAttr = attr
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func chownIfRoot(root string, uid, gid int) {
	if os.Geteuid() != 0 {
		return
	}
	_ = filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err == nil {
			_ = os.Chown(p, uid, gid)
		}
		return nil
	})
}
