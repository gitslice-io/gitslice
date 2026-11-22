package remoteshell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/peterh/liner"
	"gitslice.io/gitslice/internal/workspace"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiBlue  = "\x1b[34m"
	ansiCyan  = "\x1b[36m"
)

// MutationResult reports the changeset landed by a remote shell mutation.
type MutationResult struct {
	Changeset    string
	ChangesetURL string
}

// Mutator applies remote file mutations on behalf of the interactive shell.
type Mutator interface {
	WriteFile(ctx context.Context, globalPath string, data []byte) (MutationResult, error)
	Apply(ctx context.Context, operation string, edits []*corev1.FileEdit) (MutationResult, error)
}

// Config configures a remote interactive shell session.
type Config struct {
	Stdout        io.Writer
	Stderr        io.Writer
	Repo          corev1.RepositoryServiceClient
	Mutator       Mutator
	Root          string
	Cwd           string
	CommitID      string
	Scope         string
	Scoped        bool
	SyntheticDirs map[string]*corev1.TreeEntry
	Projection    []string
	Color         bool
	StickyHeader  bool
	// OutsideSliceError optionally wraps out-of-scope path errors with a
	// CLI-specific error type.
	OutsideSliceError func(message, hint string) error
}

// Shell drives an interactive Gitslice server-side repository shell.
type Shell struct {
	stdout            io.Writer
	stderr            io.Writer
	repo              corev1.RepositoryServiceClient
	mutator           Mutator
	root              string
	cwd               string
	commitID          string
	scope             string
	scoped            bool
	syntheticDirs     map[string]*corev1.TreeEntry
	projection        []string
	color             bool
	stickyHeader      bool
	headerActive      bool
	outsideSliceError func(message, hint string) error
}

// New constructs a Shell from cfg.
func New(cfg Config) *Shell {
	cwd := cfg.Cwd
	if cwd == "" {
		cwd = cfg.Root
	}
	return &Shell{
		stdout:            cfg.Stdout,
		stderr:            cfg.Stderr,
		repo:              cfg.Repo,
		mutator:           cfg.Mutator,
		root:              cfg.Root,
		cwd:               cwd,
		commitID:          cfg.CommitID,
		scope:             cfg.Scope,
		scoped:            cfg.Scoped,
		syntheticDirs:     cfg.SyntheticDirs,
		projection:        cfg.Projection,
		color:             cfg.Color,
		stickyHeader:      cfg.StickyHeader,
		outsideSliceError: cfg.OutsideSliceError,
	}
}

var shellCommands = []string{
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

// RunScanner executes commands read line-by-line from input.
func (s *Shell) RunScanner(ctx context.Context, input io.Reader, quiet bool) error {
	scanner := bufio.NewScanner(input)
	for {
		if !quiet {
			s.refreshHeader()
			fmt.Fprint(s.stdout, s.prompt())
		}
		if !scanner.Scan() {
			break
		}
		done, err := s.Exec(ctx, scanner.Text())
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

// RunLineEditor runs an interactive line editor with history and tab completion.
func (s *Shell) RunLineEditor(ctx context.Context) error {
	line := liner.NewLiner()
	defer line.Close()
	line.SetCompleter(func(input string) []string {
		return s.CompleteLine(ctx, input)
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
		done, err := s.Exec(ctx, input)
		if err != nil {
			fmt.Fprintf(s.stderr, "%s: %v\n", s.colorize(ansiRed, "error"), err)
			continue
		}
		if done {
			return nil
		}
	}
}

// Exec executes one command line, reporting whether the shell should exit.
func (s *Shell) Exec(ctx context.Context, line string) (bool, error) {
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

// PrintHeader writes the shell banner (or initializes the sticky terminal header).
func (s *Shell) PrintHeader() {
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

// RestoreHeader resets the terminal scroll region if sticky headers were active.
func (s *Shell) RestoreHeader() {
	if !s.stickyHeader || !s.headerActive {
		return
	}
	fmt.Fprint(s.stdout, "\x1b[r\n")
	s.headerActive = false
}

func (s *Shell) refreshHeader() {
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

func (s *Shell) printHelp() {
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

func (s *Shell) prompt() string {
	return s.promptWithColor(s.color)
}

func (s *Shell) plainPrompt() string {
	return s.promptWithColor(false)
}

func (s *Shell) promptWithColor(enabled bool) string {
	if !s.scoped {
		return fmt.Sprintf("%s %s> ", colorize(enabled, ansiDim, "gs"), colorize(enabled, ansiCyan, s.shellPath(s.cwd)))
	}
	return fmt.Sprintf("%s %s:%s> ", colorize(enabled, ansiDim, "gs"), colorize(enabled, ansiGreen, s.scope), colorize(enabled, ansiCyan, s.shellPath(s.cwd)))
}

func (s *Shell) shellPath(globalPath string) string {
	if !s.scoped {
		cleaned, err := workspace.CleanGlobalPath(globalPath)
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

func (s *Shell) entryName(entry *corev1.TreeEntry) string {
	if entry == nil {
		return ""
	}
	if entry.Name != "" {
		return entry.Name
	}
	return path.Base(entry.Path)
}

func (s *Shell) colorize(code, value string) string {
	return colorize(s.color, code, value)
}

func colorize(enabled bool, code, value string) string {
	if !enabled {
		return value
	}
	return code + value + ansiReset
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
