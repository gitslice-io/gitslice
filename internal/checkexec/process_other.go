//go:build !unix

package checkexec

import "os/exec"

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return killProcessGroup(cmd)
	}
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
