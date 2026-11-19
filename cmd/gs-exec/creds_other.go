//go:build !unix

package main

import "os/exec"

func configureChildProcess(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func chownIfRoot(_ string, _, _ int) {}
