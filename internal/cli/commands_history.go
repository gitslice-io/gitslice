package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// historyCommands builds gs log, gs show, gs diff and gs ci.
func (r Runner) historyCommands(opts *commandOptions) []*cobra.Command {
	logLimit := 20
	logPath := ""
	logSlice := ""
	logPageToken := ""
	logNoFollowMoves := false
	logFollow := false
	logAll := false
	logFull := false
	logOneline := false
	logNameOnly := false
	logStat := false
	logPatch := false
	logCmd := &cobra.Command{
		Use:   "log [-- <path>]",
		Short: "List native commits",
		Args:  maxArgs(1, "gs log [-- <path>] [--slice slice|account:slice] [--limit 20]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			pathFilter := logPath
			if len(args) == 1 {
				if strings.TrimSpace(pathFilter) != "" {
					return userError("invalid_args", "path was provided twice", "Pass either gs log -- <path> or gs log --path <path>.")
				}
				pathFilter = args[0]
			}
			return r.runCommitLog(cmd.Context(), *opts, commitLogOptions{
				Limit:         logLimit,
				Path:          pathFilter,
				Slice:         logSlice,
				PageToken:     logPageToken,
				NoFollowMoves: logNoFollowMoves,
				Follow:        logFollow,
				All:           logAll,
				Full:          logFull,
				Oneline:       logOneline,
				NameOnly:      logNameOnly,
				Stat:          logStat,
				Patch:         logPatch,
				IncludeScope:  true,
			})
		},
	}
	logCmd.Flags().IntVar(&logLimit, "limit", logLimit, "maximum commits to list")
	logCmd.Flags().StringVar(&logPath, "path", logPath, "absolute path, or workspace-relative path inside a workspace")
	logCmd.Flags().StringVar(&logSlice, "slice", logSlice, "slice to filter commits, defaults to workspace slice or personal home slice")
	logCmd.Flags().StringVar(&logPageToken, "page-token", logPageToken, "opaque pagination token from a previous log")
	logCmd.Flags().BoolVar(&logNoFollowMoves, "no-follow-moves", logNoFollowMoves, "show literal path history without following moves")
	logCmd.Flags().BoolVar(&logFollow, "follow", logFollow, "follow file or directory identity across moves")
	logCmd.Flags().BoolVar(&logAll, "all", logAll, "list all readable history instead of defaulting to a slice")
	logCmd.Flags().BoolVar(&logFull, "full", logFull, "show full native commit ids")
	logCmd.Flags().BoolVar(&logOneline, "oneline", logOneline, "use the default one-line format")
	logCmd.Flags().BoolVar(&logNameOnly, "name-only", logNameOnly, "show only changed path names under each commit")
	logCmd.Flags().BoolVar(&logStat, "stat", logStat, "show a compact changed-path summary under each commit")
	logCmd.Flags().BoolVarP(&logPatch, "patch", "p", logPatch, "show patches for each commit")

	showFull := false
	showNameOnly := false
	showStat := false
	showPatch := false
	showCmd := &cobra.Command{
		Use:   "show <commit-id-or-prefix>",
		Short: "Show a native commit",
		Args:  exactArgs(1, "gs show <commit-id-or-prefix>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runCommitShow(cmd.Context(), *opts, args[0], commitShowOptions{
				Full:     showFull,
				NameOnly: showNameOnly,
				Stat:     showStat,
				Patch:    showPatch,
			})
		},
	}
	showCmd.Flags().BoolVar(&showFull, "full", showFull, "show full native commit ids")
	showCmd.Flags().BoolVar(&showNameOnly, "name-only", showNameOnly, "show only changed path names")
	showCmd.Flags().BoolVar(&showStat, "stat", showStat, "show a compact changed-path summary")
	showCmd.Flags().BoolVarP(&showPatch, "patch", "p", showPatch, "show the commit patch")

	diffNameOnly := false
	diffStat := false
	diffFrom := ""
	diffTo := ""
	diffCmd := &cobra.Command{
		Use:   "diff [commit-id-or-prefix] [commit-id-or-prefix]",
		Short: "Show workspace, changeset, or commit diffs",
		Args:  maxArgs(2, "gs diff [commit-id-or-prefix] [commit-id-or-prefix] [--from patchset --to patchset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runDiff(cmd.Context(), *opts, args, diffFrom, diffTo, diffNameOnly, diffStat)
		},
	}
	diffCmd.Flags().StringVar(&diffFrom, "from", diffFrom, "patchset number or id to diff from")
	diffCmd.Flags().StringVar(&diffTo, "to", diffTo, "patchset number or id to diff to")
	diffCmd.Flags().BoolVar(&diffNameOnly, "name-only", diffNameOnly, "show only changed path names")
	diffCmd.Flags().BoolVar(&diffStat, "stat", diffStat, "show a compact changed-path summary")

	ciCmd := &cobra.Command{
		Use:   "ci",
		Short: "Run workspace checks locally",
		Args:  noArgs("gs ci"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runCI(cmd.Context(), *opts)
		},
	}

	return []*cobra.Command{logCmd, showCmd, diffCmd, ciCmd}
}
