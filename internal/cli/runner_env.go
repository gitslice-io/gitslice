package cli

import (
	"io"
	"os"
	"path/filepath"

	"gitslice.io/gitslice/internal/clientcache"
	"golang.org/x/term"
)

func (r Runner) stdin() io.Reader {
	if r.Stdin != nil {
		return r.Stdin
	}
	return os.Stdin
}

func (r Runner) stdout() io.Writer {
	if r.Stdout != nil {
		return r.Stdout
	}
	return os.Stdout
}

func (r Runner) stderr() io.Writer {
	if r.Stderr != nil {
		return r.Stderr
	}
	return os.Stderr
}

func (r Runner) colorEnabled(opts commandOptions) bool {
	if opts.NoColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(r.stdout())
}

func (r Runner) stickyShellHeaderEnabled(opts commandOptions) bool {
	if opts.NoColor || opts.Quiet || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(r.stdout()) && isTerminal(r.stdin())
}

func (r Runner) shellLineEditorEnabled(opts commandOptions) bool {
	if opts.Quiet || opts.NonInteractive || os.Getenv("TERM") == "dumb" {
		return false
	}
	return r.stdin() == os.Stdin && r.stdout() == os.Stdout && isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func isTerminal(w any) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func (r Runner) objectCache() (*clientcache.ObjectCache, error) {
	root := os.Getenv("GS_CLIENT_CACHE_DIR")
	if root == "" {
		root = os.Getenv("GITSLICE_CLIENT_CACHE_DIR")
	}
	if root == "" {
		root = r.defaultObjectCacheRoot()
	}
	return clientcache.New(root)
}

func (r Runner) defaultObjectCacheRoot() string {
	if r.Home != "" {
		return filepath.Join(r.Home, ".cache", "gitslice")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "gitslice")
	}
	return filepath.Join(r.homeDir(), ".cache", "gitslice")
}

func (r Runner) homeDir() string {
	if r.Home != "" {
		return r.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

func (r Runner) cwd() string {
	if r.Dir != "" {
		return r.Dir
	}
	dir, _ := os.Getwd()
	return dir
}
