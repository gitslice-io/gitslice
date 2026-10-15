package cli

import (
	"fmt"
	"io"
)

func printPathList(w io.Writer, paths []string) {
	for _, p := range paths {
		fmt.Fprintln(w, p)
	}
}

func printPathStat(w io.Writer, paths []string) {
	fmt.Fprintf(w, "%d changed path(s)\n", len(paths))
	for _, p := range paths {
		fmt.Fprintf(w, "  %s\n", p)
	}
}

func printIndentedPaths(w io.Writer, paths []string, indent string) {
	if len(paths) == 0 {
		fmt.Fprintln(w, indent+"none")
		return
	}
	for _, p := range paths {
		fmt.Fprintln(w, indent+p)
	}
}

func printPathListColor(w io.Writer, paths []string, color bool) {
	for _, p := range paths {
		fmt.Fprintln(w, colorize(color, ansiBlue, p))
	}
}

func printPathStatColor(w io.Writer, paths []string, color bool) {
	fmt.Fprintf(w, "%d changed path(s)\n", len(paths))
	printIndentedPathsColor(w, paths, "  ", color)
}

func printIndentedPathsColor(w io.Writer, paths []string, indent string, color bool) {
	if len(paths) == 0 {
		fmt.Fprintln(w, indent+"none")
		return
	}
	for _, p := range paths {
		fmt.Fprintln(w, indent+colorize(color, ansiBlue, p))
	}
}
