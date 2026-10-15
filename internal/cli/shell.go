package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/peterh/liner"
	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (r Runner) runShell(ctx context.Context, opts commandOptions, commitID, sliceRef string) error {
	if opts.jsonOutput() {
		return userError("unsupported_format", "gs shell only supports text output", "Run gs shell without --json or --format json.")
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	rootPath, scopeLabel, workspaceScoped, syntheticDirs, mutationSlice, err := r.shellScope(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	pinnedCommit := commitID != ""
	if commitID == "" {
		ref, err := repo.GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
		if err != nil {
			return err
		}
		commitID = ref.CommitId
	}
	var mutator *remoteFileMutator
	if mutationSlice != nil && !pinnedCommit {
		mutator = &remoteFileMutator{runner: r, cfg: cfg, conn: conn, slice: mutationSlice}
	}
	projectionRoots, err := shellProjectionRoots(sliceRef, mutationSlice, workspaceScoped)
	if err != nil {
		return err
	}
	sh := &serverShell{
		runner:        r,
		stdout:        r.stdout(),
		stderr:        r.stderr(),
		repo:          repo,
		mutator:       mutator,
		root:          rootPath,
		cwd:           rootPath,
		commitID:      commitID,
		scope:         scopeLabel,
		scoped:        workspaceScoped,
		syntheticDirs: syntheticDirs,
		projection:    projectionRoots,
		color:         r.colorEnabled(opts),
		stickyHeader:  r.stickyShellHeaderEnabled(opts),
	}
	if !opts.Quiet {
		sh.printHeader()
		defer sh.restoreHeader()
	}
	if r.shellLineEditorEnabled(opts) {
		return sh.runLineEditor(callCtx)
	}
	return sh.runScanner(callCtx, r.stdin(), opts.Quiet)
}

func (s *serverShell) runScanner(ctx context.Context, input io.Reader, quiet bool) error {
	scanner := bufio.NewScanner(input)
	for {
		if !quiet {
			s.refreshHeader()
			fmt.Fprint(s.stdout, s.prompt())
		}
		if !scanner.Scan() {
			break
		}
		done, err := s.exec(ctx, scanner.Text())
		if err != nil {
			fmt.Fprintf(s.stderr, "%s: %v\n", s.colorize(ansiRed, "error"), err)
			continue
		}
		if done {
			return nil
		}
	}
	return scanner.Err()
}

func (s *serverShell) runLineEditor(ctx context.Context) error {
	line := liner.NewLiner()
	defer line.Close()
	line.SetCompleter(func(input string) []string {
		return s.completeLine(ctx, input)
	})
	for {
		s.refreshHeader()
		input, err := line.Prompt(s.plainPrompt())
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(input) != "" {
			line.AppendHistory(input)
		}
		done, err := s.exec(ctx, input)
		if err != nil {
			fmt.Fprintf(s.stderr, "%s: %v\n", s.colorize(ansiRed, "error"), err)
			continue
		}
		if done {
			return nil
		}
	}
}

type serverShell struct {
	runner        Runner
	stdout        io.Writer
	stderr        io.Writer
	repo          corev1.RepositoryServiceClient
	mutator       *remoteFileMutator
	root          string
	cwd           string
	commitID      string
	scope         string
	scoped        bool
	syntheticDirs map[string]*corev1.TreeEntry
	projection    []string
	color         bool
	stickyHeader  bool
	headerActive  bool
}

var serverShellCommands = []string{
	"?",
	"cat",
	"cd",
	"exit",
	"help",
	"ls",
	"mkdir",
	"mv",
	"pwd",
	"ref",
	"rm",
	"stat",
	"touch",
	"quit",
	"write",
}

func (s *serverShell) exec(ctx context.Context, line string) (bool, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false, nil
	}
	switch fields[0] {
	case "exit", "quit":
		return true, nil
	case "help", "?":
		s.printHelp()
	case "pwd":
		if len(fields) != 1 {
			return false, fmt.Errorf("usage: pwd")
		}
		fmt.Fprintln(s.stdout, s.shellPath(s.cwd))
	case "ref":
		if len(fields) != 1 {
			return false, fmt.Errorf("usage: ref")
		}
		fmt.Fprintln(s.stdout, s.commitID)
	case "cd":
		target := "."
		if len(fields) > 2 {
			return false, fmt.Errorf("usage: cd [path]")
		}
		if len(fields) == 2 {
			target = fields[1]
		}
		return false, s.cd(ctx, target)
	case "ls":
		target := "."
		if len(fields) > 2 {
			return false, fmt.Errorf("usage: ls [path]")
		}
		if len(fields) == 2 {
			target = fields[1]
		}
		return false, s.ls(ctx, target)
	case "cat":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: cat <file>")
		}
		return false, s.cat(ctx, fields[1])
	case "stat":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: stat <path>")
		}
		return false, s.stat(ctx, fields[1])
	case "mkdir":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: mkdir <path>")
		}
		p, err := s.resolve(fields[1])
		if err != nil {
			return false, err
		}
		return false, s.mutate(ctx, "mkdir", []*corev1.FileEdit{{Op: "mkdir", Path: p}})
	case "touch":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: touch <file>")
		}
		return false, s.write(ctx, fields[1], nil)
	case "write":
		if len(fields) < 3 {
			return false, fmt.Errorf("usage: write <file> <text>")
		}
		return false, s.write(ctx, fields[1], []byte(strings.Join(fields[2:], " ")))
	case "mv":
		if len(fields) != 3 {
			return false, fmt.Errorf("usage: mv <old-path> <new-path>")
		}
		oldPath, err := s.resolve(fields[1])
		if err != nil {
			return false, err
		}
		newPath, err := s.resolve(fields[2])
		if err != nil {
			return false, err
		}
		return false, s.mutate(ctx, "mv", []*corev1.FileEdit{{Op: "rename", OldPath: oldPath, Path: newPath}})
	case "rm":
		if len(fields) != 2 {
			return false, fmt.Errorf("usage: rm <path>")
		}
		p, err := s.resolve(fields[1])
		if err != nil {
			return false, err
		}
		return false, s.mutate(ctx, "rm", []*corev1.FileEdit{{Op: "delete", Path: p}})
	default:
		return false, fmt.Errorf("unknown command %q", fields[0])
	}
	return false, nil
}

func (s *serverShell) printHeader() {
	if s.stickyHeader {
		s.headerActive = true
		fmt.Fprint(s.stdout, "\x1b[2J\x1b[H")
		s.refreshHeader()
		fmt.Fprint(s.stdout, "\x1b[4;r\x1b[4;1H")
		return
	}
	fmt.Fprintf(s.stdout, "%s: %s @ %s\n", s.colorize(ansiGreen, "server shell"), s.scope, shortID(s.commitID))
	fmt.Fprintf(s.stdout, "%s: %s\n", s.colorize(ansiDim, "cwd"), s.shellPath(s.cwd))
	fmt.Fprintln(s.stdout, s.colorize(ansiDim, "type help for commands"))
}

func (s *serverShell) refreshHeader() {
	if !s.stickyHeader || !s.headerActive {
		return
	}
	mode := "read-only"
	if s.mutator != nil {
		mode = "mutable"
	}
	scopeLabel := s.scope
	if scopeLabel == "/" {
		scopeLabel = "global root"
	}
	fmt.Fprint(s.stdout, "\x1b[s")
	fmt.Fprint(s.stdout, "\x1b[r")
	fmt.Fprint(s.stdout, "\x1b[1;1H\x1b[2K")
	fmt.Fprintf(s.stdout, "%s  %s %s  %s %s  %s %s\n",
		s.colorize(ansiGreen, "Gitslice shell"),
		s.colorize(ansiDim, "slice:"), scopeLabel,
		s.colorize(ansiDim, "commit:"), shortID(s.commitID),
		s.colorize(ansiDim, "mode:"), mode)
	fmt.Fprint(s.stdout, "\x1b[2K")
	fmt.Fprintf(s.stdout, "%s %s  %s %s  %s\n",
		s.colorize(ansiDim, "cwd:"), s.shellPath(s.cwd),
		s.colorize(ansiDim, "root:"), s.shellPath(s.root),
		s.colorize(ansiDim, "help: type help, exit to quit"))
	fmt.Fprint(s.stdout, "\x1b[2K")
	fmt.Fprintln(s.stdout, s.colorize(ansiDim, strings.Repeat("-", 72)))
	fmt.Fprint(s.stdout, "\x1b[4;r")
	fmt.Fprint(s.stdout, "\x1b[u")
}

func (s *serverShell) restoreHeader() {
	if !s.stickyHeader || !s.headerActive {
		return
	}
	fmt.Fprint(s.stdout, "\x1b[r\n")
	s.headerActive = false
}

func (s *serverShell) printHelp() {
	fmt.Fprintln(s.stdout, "commands:")
	fmt.Fprintln(s.stdout, "  pwd              print current server path")
	fmt.Fprintln(s.stdout, "  ls [path]        list a server directory")
	fmt.Fprintln(s.stdout, "  cd [path]        change server directory")
	fmt.Fprintln(s.stdout, "  cat <file>       print a server file")
	fmt.Fprintln(s.stdout, "  stat <path>      inspect server file or directory metadata")
	fmt.Fprintln(s.stdout, "  mkdir <path>     create a server directory")
	fmt.Fprintln(s.stdout, "  write <file> <text>")
	fmt.Fprintln(s.stdout, "                   create or replace a server file")
	fmt.Fprintln(s.stdout, "  touch <file>     create an empty server file")
	fmt.Fprintln(s.stdout, "  mv <old> <new>   rename or move a server file or directory")
	fmt.Fprintln(s.stdout, "  rm <path>        delete a server file or directory")
	fmt.Fprintln(s.stdout, "  ref              print inspected commit id")
	fmt.Fprintln(s.stdout, "  help             show this help")
	fmt.Fprintln(s.stdout, "  exit, quit       leave the shell")
	fmt.Fprintln(s.stdout, "  <tab>            complete commands and server paths")
}

func (s *serverShell) prompt() string {
	return s.promptWithColor(s.color)
}

func (s *serverShell) plainPrompt() string {
	return s.promptWithColor(false)
}

func (s *serverShell) promptWithColor(enabled bool) string {
	if !s.scoped {
		return fmt.Sprintf("%s %s> ", colorize(enabled, ansiDim, "gs"), colorize(enabled, ansiCyan, s.shellPath(s.cwd)))
	}
	return fmt.Sprintf("%s %s:%s> ", colorize(enabled, ansiDim, "gs"), colorize(enabled, ansiGreen, s.scope), colorize(enabled, ansiCyan, s.shellPath(s.cwd)))
}

func (s *serverShell) shellPath(globalPath string) string {
	if !s.scoped {
		cleaned, err := cleanShellGlobalPath(globalPath)
		if err != nil {
			return globalPath
		}
		return cleaned
	}
	if globalPath == s.root {
		return "/"
	}
	rel := strings.TrimPrefix(globalPath, strings.TrimRight(s.root, "/")+"/")
	return "/" + rel
}

func cleanShellGlobalPath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "/", nil
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "/", nil
	}
	return cleaned, nil
}

func (s *serverShell) entryName(entry *corev1.TreeEntry) string {
	if entry == nil {
		return ""
	}
	if entry.Name != "" {
		return entry.Name
	}
	return path.Base(entry.Path)
}

func (s *serverShell) colorize(code, value string) string {
	return colorize(s.color, code, value)
}
