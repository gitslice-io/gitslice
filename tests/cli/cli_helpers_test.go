package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/cli"
)

func runCLI(t *testing.T, home, workspace string, args ...string) string {
	t.Helper()
	stdout, _ := runCLIStreams(t, home, workspace, args...)
	return stdout
}

func runCLIStreams(t *testing.T, home, workspace string, args ...string) (string, string) {
	t.Helper()
	return runCLIStreamsWithInput(t, home, workspace, "", args...)
}

func runCLIStreamsWithInput(t *testing.T, home, workspace, stdin string, args ...string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		t.Fatalf("gs %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func runCLIFails(t *testing.T, home, workspace string, args ...string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err == nil {
		t.Fatalf("gs %s unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), stdout.String(), stderr.String())
	} else {
		fmt.Fprintln(&stderr, err)
	}
	return stdout.String(), stderr.String()
}

func runCLIResult(home, workspace string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		return stdout.String(), fmt.Errorf("gs %s failed: %w\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), nil
}

// runCLISignupThroughWeb provisions a brand-new account for username and writes
// the CLI auth config so subsequent gs commands in `home` run as that user. It
// mints a service token and calls ChooseUsername (the dev signup flow is gone);
// the username is normalized to a slug exactly as the old signup path did.
func runCLISignupThroughWeb(t *testing.T, home, dir string, ts *testServer, username string) {
	t.Helper()
	_ = dir
	token, _, subjectID := ts.provisionAccount(t, username, username)
	writeCLIAuthConfig(t, home, ts.addr, token, subjectID)
}

func readSignupApprovalURL(t *testing.T, r io.Reader) string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("approval URL was not printed")
	return ""
}
