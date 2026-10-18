package fusefs

import "strings"

// platformMountOptions are macFUSE's options for a workspace: Finder's
// AppleDouble (._*) and .DS_Store files and Apple's extended attributes would
// otherwise turn up as local edits. The volume is named after the slice
// (Finder treats ":" and "/" as separators, so they become spaces).
func platformMountOptions(opts Options) []string {
	options := []string{"noappledouble", "noapplexattr"}
	if opts.FsName != "" {
		options = append(options, "volname="+strings.NewReplacer(":", " ", "/", " ", ",", " ").Replace(opts.FsName))
	}
	return options
}
