// Command mirror exports a slice's projected Git history from Gitslice to a
// Git mirror such as the GitHub repository (design/21_self_hosting.md).
//
//	mirror -repo . -source https://gitslice.io/git/gitslice/gitslice.git \
//	       -subdir gitslice/gitslice -push -new-tags-out tags.txt
//
// GITSLICE_MIRROR_TOKEN, when set, authenticates the source fetch.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	cfg := config{}
	newTagsOut := ""
	flag.StringVar(&cfg.Repo, "repo", ".", "clone of the mirror repository")
	flag.StringVar(&cfg.Remote, "remote", "origin", "mirror remote")
	flag.StringVar(&cfg.Branch, "branch", "main", "mirror branch")
	flag.StringVar(&cfg.Source, "source", "", "Gitslice Git URL of the slice")
	flag.StringVar(&cfg.Subdir, "subdir", "", "slice path inside the projected repository, e.g. gitslice/gitslice")
	flag.BoolVar(&cfg.Push, "push", false, "push new commits and tags to the mirror")
	flag.StringVar(&newTagsOut, "new-tags-out", "", "write newly pushed tag names to this file, one per line")
	flag.Parse()
	if cfg.Source == "" {
		fmt.Fprintln(os.Stderr, "mirror: -source is required")
		os.Exit(2)
	}
	cfg.Token = os.Getenv("GITSLICE_MIRROR_TOKEN")
	res, err := run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mirror:", err)
		os.Exit(1)
	}
	fmt.Printf("mirror head %s, %d new commit(s), %d new tag(s)\n", short(res.Head), len(res.Exported), len(res.NewTags))
	if newTagsOut != "" {
		content := strings.Join(res.NewTags, "\n")
		if content != "" {
			content += "\n"
		}
		if err := os.WriteFile(newTagsOut, []byte(content), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "mirror:", err)
			os.Exit(1)
		}
	}
}
