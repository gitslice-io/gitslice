package cli

import (
	"github.com/spf13/cobra"
)

// stackCommands builds the top-level changeset and dependency commands: create, modify, submit, deps, update-dependents, switch, up, down, top, bottom, move, insert and detach.
func (r Runner) stackCommands(opts *commandOptions) []*cobra.Command {
	createMessage := ""
	createDescription := ""
	createDescriptionFile := ""
	createParent := ""
	createRoot := false
	createSibling := false
	createAll := false
	createSubmit := false
	createWatchTimeout := "15m"
	createEmpty := emptyChangesetOptions{}
	createReview := reviewOptions{}
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a changeset from workspace edits",
		Long: "Create a changeset from workspace edits. With --submit, also submit it: gs create waits for " +
			"required checks that have no result yet (up to --watch-timeout), submits, and waits for it to land. " +
			"A file's path in the changeset is its path in the workspace; no flag re-bases paths. When files would be " +
			"added to a directory that is new, gs create lists those directories first, and asks before adding " +
			"100 or more files to one (--yes skips the question); it refuses when files would repeat the slice's own path.",
		Args: noArgs("gs create --message title [--description text] [--all] [--submit] [--if-changed | --allow-empty] [--base changeset] [--sibling] [--root] [--yes]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			submit := createSubmitOptions{}
			if createSubmit {
				timeout, err := parsePositiveDurationFlag("watch-timeout", createWatchTimeout)
				if err != nil {
					return err
				}
				submit = createSubmitOptions{enabled: true, timeout: timeout}
			}
			description, err := changesetDescriptionFlag(r.Stdin, createDescription, createDescriptionFile)
			if err != nil {
				return err
			}
			return r.runStackCreate(cmd.Context(), *opts, createMessage, description, createParent, createRoot, createSibling, createAll, submit, createEmpty, createReview)
		},
	}
	createCmd.Flags().StringVar(&createMessage, "message", createMessage, "changeset title: one line, like a commit subject. A message of several lines is split: the first line is the title, the rest the description")
	createCmd.Flags().StringVar(&createDescription, "description", createDescription, "changeset description (Markdown): what and why, shown on its page and, after the title, in the commit message")
	createCmd.Flags().StringVar(&createDescriptionFile, "description-file", createDescriptionFile, "read the description from a file, or from standard input with -")
	createCmd.Flags().StringVar(&createParent, "base", createParent, "base changeset id or handle")
	createCmd.Flags().BoolVar(&createRoot, "root", createRoot, "start a new dependency tree instead of stacking on the active changeset (file paths are not affected)")
	createCmd.Flags().BoolVar(&createSibling, "sibling", createSibling, "create a sibling of the active changeset")
	createCmd.Flags().BoolVar(&createAll, "all", createAll, "snapshot all current workspace edits")
	createCmd.Flags().BoolVar(&createSubmit, "submit", createSubmit, "submit the changeset once required checks pass, and wait for it to land")
	createCmd.Flags().StringVar(&createWatchTimeout, "watch-timeout", createWatchTimeout, "with --submit, how long to wait for required checks and for the changeset to land")
	createEmpty.addFlags(createCmd)
	createReview.addFlags(createCmd)

	modifyMessage := ""
	modifyNoRestack := false
	modifyAll := false
	modifyCmd := &cobra.Command{
		Use:   "modify",
		Short: "Create a new patchset for the active changeset",
		Args:  noArgs("gs modify [--all] [--message title] [--no-update-dependents]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStackModify(cmd.Context(), *opts, modifyMessage, modifyNoRestack, modifyAll)
		},
	}
	modifyCmd.Flags().StringVar(&modifyMessage, "message", modifyMessage, "replacement title is reserved for future use")
	modifyCmd.Flags().BoolVar(&modifyNoRestack, "no-update-dependents", modifyNoRestack, "do not update dependent changesets after modifying")
	modifyCmd.Flags().BoolVar(&modifyAll, "all", modifyAll, "snapshot all current workspace edits")

	submitStack := false
	submitSubtree := ""
	submitNoWatch := false
	submitWatchTimeout := "5m"
	submitCmd := &cobra.Command{
		Use:   "submit [changeset]",
		Short: "Submit the active changeset or dependent changesets",
		Args:  maxArgs(1, "gs submit [changeset] [--with-dependencies] [--subtree changeset] [--no-watch]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			watchTimeout, err := parsePositiveDurationFlag("watch-timeout", submitWatchTimeout)
			if err != nil {
				return err
			}
			return r.runStackSubmit(cmd.Context(), *opts, id, submitStack, submitSubtree, !submitNoWatch, watchTimeout)
		},
	}
	submitCmd.Flags().BoolVar(&submitStack, "with-dependencies", submitStack, "submit dependent changesets together")
	submitCmd.Flags().StringVar(&submitSubtree, "subtree", submitSubtree, "submit one dependency subtree")
	submitCmd.Flags().BoolVar(&submitNoWatch, "no-watch", submitNoWatch, "return after submit is accepted without waiting for publish")
	submitCmd.Flags().StringVar(&submitWatchTimeout, "watch-timeout", submitWatchTimeout, "maximum time to wait for required checks that have not reported, and for publish")

	depsCmd := &cobra.Command{
		Use:   "deps [dependency-tree]",
		Short: "Show dependent changesets",
		Args:  maxArgs(1, "gs deps [dependency-tree]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runStackShow(cmd.Context(), *opts, id)
		},
	}

	restackChildren := false
	restackAll := false
	updateDependentsCmd := &cobra.Command{
		Use:   "update-dependents [changeset]",
		Short: "Replay dependent changesets onto current base patchsets",
		Args:  maxArgs(1, "gs update-dependents [changeset] [--children|--all]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			entry := ""
			if len(args) > 0 {
				entry = args[0]
			}
			return r.runStackRestack(cmd.Context(), *opts, entry, restackChildren, restackAll)
		},
	}
	updateDependentsCmd.Flags().BoolVar(&restackChildren, "children", restackChildren, "update the selected changeset and dependents")
	updateDependentsCmd.Flags().BoolVar(&restackAll, "all", restackAll, "update all sibling dependency subtrees")

	switchCmd := &cobra.Command{
		Use:   "switch <changeset>",
		Short: "Set the active changeset",
		Args:  exactArgs(1, "gs switch <changeset>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStackSwitch(cmd.Context(), *opts, args[0])
		},
	}

	upCmd := &cobra.Command{
		Use:   "up [changeset]",
		Short: "Switch to a dependent changeset",
		Args:  maxArgs(1, "gs up [changeset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			entry := ""
			if len(args) > 0 {
				entry = args[0]
			}
			return r.runStackUp(cmd.Context(), *opts, entry)
		},
	}

	downCmd := &cobra.Command{
		Use:   "down [steps]",
		Short: "Switch to a base changeset",
		Args:  maxArgs(1, "gs down [steps]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			steps := 1
			if len(args) > 0 {
				var err error
				steps, err = parsePositiveIntArg("steps", args[0])
				if err != nil {
					return err
				}
			}
			return r.runStackDown(cmd.Context(), *opts, steps)
		},
	}

	topCmd := &cobra.Command{
		Use:   "top",
		Short: "Switch to the top dependent changeset on the current path",
		Args:  noArgs("gs top"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStackTop(cmd.Context(), *opts)
		},
	}

	bottomCmd := &cobra.Command{
		Use:   "bottom",
		Short: "Switch to the root changeset",
		Args:  noArgs("gs bottom"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStackBottom(cmd.Context(), *opts)
		},
	}

	moveOnto := ""
	moveSiblingOrder := int64(0)
	moveCmd := &cobra.Command{
		Use:   "move <changeset> --onto <base|root>",
		Short: "Move a changeset to a new base",
		Args:  exactArgs(1, "gs move <changeset> --onto <base|root>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStackMove(cmd.Context(), *opts, args[0], moveOnto, moveSiblingOrder)
		},
	}
	moveCmd.Flags().StringVar(&moveOnto, "onto", moveOnto, "new base changeset or root")
	moveCmd.Flags().Int64Var(&moveSiblingOrder, "order", moveSiblingOrder, "sibling order under the new base")

	insertParent := ""
	insertMessage := ""
	insertCmd := &cobra.Command{
		Use:   "insert --base <changeset> --message <title>",
		Short: "Insert a new base changeset before the active dependent",
		Args:  noArgs("gs insert --base <changeset> --message <title>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStackInsert(cmd.Context(), *opts, insertParent, insertMessage)
		},
	}
	insertCmd.Flags().StringVar(&insertParent, "base", insertParent, "base changeset id or handle")
	insertCmd.Flags().StringVar(&insertMessage, "message", insertMessage, "changeset title")

	detachMessage := ""
	detachCmd := &cobra.Command{
		Use:   "detach <changeset>",
		Short: "Detach a dependent changeset into a new dependency tree",
		Args:  exactArgs(1, "gs detach <changeset>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStackDetach(cmd.Context(), *opts, args[0], detachMessage)
		},
	}
	detachCmd.Flags().StringVar(&detachMessage, "message", detachMessage, "new dependency title")

	return []*cobra.Command{createCmd, modifyCmd, submitCmd, depsCmd, updateDependentsCmd, switchCmd, upCmd, downCmd, topCmd, bottomCmd, moveCmd, insertCmd, detachCmd}
}
