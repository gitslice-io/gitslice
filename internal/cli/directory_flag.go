package cli

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractDirectoryFlag takes gs's -C <dir> (also -C<dir>, -C=<dir>,
// --directory <dir>, --directory=<dir>) out of args, as git -C does. Each
// one is relative to the directory before it, starting at cwd. It stops at
// "--". The directory must exist. dir is "" when no -C was given.
func extractDirectoryFlag(args []string, cwd string) (rest []string, dir string, err error) {
	current := cwd
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		value, ok := "", false
		switch {
		case arg == "-C" || arg == "--directory":
			if i+1 >= len(args) {
				return nil, "", userError("invalid_args", arg+" needs a directory", "Use gs -C <dir> <command>.")
			}
			value, ok = args[i+1], true
			i++
		case strings.HasPrefix(arg, "--directory="):
			value, ok = strings.TrimPrefix(arg, "--directory="), true
		case strings.HasPrefix(arg, "-C="):
			value, ok = strings.TrimPrefix(arg, "-C="), true
		case strings.HasPrefix(arg, "-C") && len(arg) > 2:
			value, ok = arg[2:], true
		}
		if !ok {
			rest = append(rest, arg)
			continue
		}
		if value == "" {
			return nil, "", userError("invalid_args", "-C needs a directory", "Use gs -C <dir> <command>.")
		}
		if value == "~" || strings.HasPrefix(value, "~/") {
			if home, herr := os.UserHomeDir(); herr == nil {
				value = path.Join(home, strings.TrimPrefix(value, "~"))
			}
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(current, value)
		}
		info, statErr := os.Stat(value)
		if statErr != nil || !info.IsDir() {
			return nil, "", userError("invalid_args", "-C "+value+": no such directory", "Pass an existing directory.")
		}
		current = filepath.Clean(value)
		dir = current
	}
	return rest, dir, nil
}

// prepareInitDir makes dir (relative to the working directory) ready for an
// eager gs init: created when missing, and required to be empty.
func (r Runner) prepareInitDir(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.cwd(), dir)
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", userError("invalid_args", "cannot create "+dir+": "+err.Error(), "")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		return "", userError("invalid_args", dir+" is not empty", "gs init checks a slice out into an empty directory.")
	}
	return dir, nil
}

// workspaceFilesLocation says where a checked-out slice's files are, relative
// to the workspace root, when the slice has a single root folder (files
// appear at their full path, such as ./acme/app). "" otherwise.
func workspaceFilesLocation(includedPaths []string) string {
	if len(includedPaths) != 1 {
		return ""
	}
	p := strings.Trim(includedPaths[0], "/")
	if p == "" {
		return ""
	}
	return "./" + p
}
