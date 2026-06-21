package cli

import (
	"bufio"
	"context"
	"io"
	"os/exec"
)

type agentRuntime interface {
	// Run executes one turn in workdir for the given prompt, calling emit for
	// each chunk of output. It must stop when ctx is cancelled.
	Run(ctx context.Context, workdir, prompt string, emit func(text string, final bool)) error
}

type codexRuntime struct {
	Binary string
}

func (r codexRuntime) Run(ctx context.Context, workdir, prompt string, emit func(text string, final bool)) error {
	binary := r.Binary
	if binary == "" {
		binary = "codex"
	}

	cmd := exec.CommandContext(ctx, binary, "exec", "--dangerously-bypass-approvals-and-sandbox", "--cd", workdir, prompt)
	cmd.Dir = workdir

	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return err
	}

	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = writer.Close()
		waitCh <- err
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		emit(scanner.Text(), false)
	}
	scanErr := scanner.Err()
	waitErr := <-waitCh
	_ = reader.Close()

	emit("", true)

	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return waitErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}
