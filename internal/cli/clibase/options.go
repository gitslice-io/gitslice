package clibase

import "github.com/spf13/cobra"

// Options holds the global flags parsed on the root `gs` command.
type Options struct {
	Format         string
	JSONFields     []string
	JQ             string
	Template       string
	Quiet          bool
	NonInteractive bool
	NoColor        bool
	Verbose        bool
	Debug          bool
	Trace          bool
	Insecure       bool
}

// JSONOutput reports whether structured machine output (--json, --jq,
// --template, or --format json) is requested.
func (o Options) JSONOutput() bool {
	return o.Format == "json" || len(o.JSONFields) > 0 || o.JQ != "" || o.Template != ""
}

// DefaultDiffContextLines is the default number of unchanged context lines
// shown around diff hunks.
const DefaultDiffContextLines = 3

// DiffContext holds the parsed -U/--unified/--context flag for diff commands.
type DiffContext struct {
	Context int
	Set     bool
}

// Lines returns the effective context line count (defaulting to 3).
func (d DiffContext) Lines() int {
	if d.Set {
		return d.Context
	}
	return DefaultDiffContextLines
}

// AddDiffContextFlag registers -U/--unified on cmd.
func AddDiffContextFlag(cmd *cobra.Command, target *int) {
	cmd.Flags().IntVarP(target, "unified", "U", DefaultDiffContextLines, "unchanged lines of context around each change; a negative number shows the whole file")
}

// DiffContextFromFlag constructs a DiffContext from cmd's --unified flag.
func DiffContextFromFlag(cmd *cobra.Command, value int) DiffContext {
	if cmd == nil || !cmd.Flags().Changed("unified") {
		return DiffContext{}
	}
	return DiffContext{Context: value, Set: true}
}
