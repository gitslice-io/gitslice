package cli

import (
	"io"
	"os"

	"gitslice.io/gitslice/internal/agentd"
	"gitslice.io/gitslice/internal/clientcache"
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

func (r Runner) objectCache() (*clientcache.ObjectCache, error) {
	return clientcache.New(r.objectCacheRoot())
}

func (r Runner) defaultObjectCacheRoot() string {
	return clientcache.DefaultRoot(r.Home, r.Getenv)
}

func (r Runner) homeDir() string {
	if r.Home != "" {
		return r.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

// Getenv reads an environment variable for this Runner. Variables in Env win;
// when Env is set, host credentials it does not name are hidden so an
// isolated child run never inherits the daemon's identity.
func (r Runner) Getenv(key string) string {
	if r.Env != nil {
		if value, ok := r.Env[key]; ok {
			return value
		}
		if agentd.IsSensitiveHostEnvKey(key, false) {
			return ""
		}
	}
	return os.Getenv(key)
}

func (r Runner) cwd() string {
	if r.Dir != "" {
		return r.Dir
	}
	dir, _ := os.Getwd()
	return dir
}
