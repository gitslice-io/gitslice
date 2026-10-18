package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// fsCommand builds gs fs, which reads and writes a personal home slice.
func (r Runner) fsCommand(opts *commandOptions) *cobra.Command {
	fsCmd := &cobra.Command{
		Use:     "fs",
		Aliases: []string{"file"},
		Short:   "Read and mutate files in the signed-in home slice",
		Long: "Read and change files in your home slice. Each change is a changeset that is submitted\n" +
			"and landed at once; --no-submit leaves it open for review instead.",
		RunE: requireSubcommand("fs"),
	}
	fsCmd.PersistentFlags().BoolVar(&opts.NoSubmit, "no-submit", false, "open the changeset for review instead of submitting it")
	fsLsCmd := &cobra.Command{
		Use:   "ls [remote-path]",
		Short: "List a remote directory or file in the signed-in home slice",
		Long: `List a remote directory or file in the signed-in home slice.

If no path is provided, gs lists the signed-in home slice root. For user nic,
that default remote path is /nic, not the local ~/ directory.

When a path is provided it must be an absolute remote path under the signed-in
home slice root, for example /nic/notes.`,
		Args: maxArgs(1, "gs fs ls [remote-path]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ""
			if len(args) > 0 {
				p = args[0]
			}
			return r.runFSList(cmd.Context(), *opts, p)
		},
	}
	fsCatCmd := &cobra.Command{
		Use:   "cat <absolute-path>",
		Short: "Print a remote file",
		Args:  exactArgs(1, "gs fs cat /account/path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFSCat(cmd.Context(), *opts, args[0])
		},
	}
	fsMkdirCmd := &cobra.Command{
		Use:   "mkdir <absolute-path>",
		Short: "Create a remote directory",
		Args:  exactArgs(1, "gs fs mkdir /account/path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFileMkdir(cmd.Context(), *opts, args[0])
		},
	}
	fsTouchCmd := &cobra.Command{
		Use:   "touch <absolute-path>",
		Short: "Create an empty remote file",
		Args:  exactArgs(1, "gs fs touch /account/path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFileWrite(cmd.Context(), *opts, args[0], nil)
		},
	}
	fsWriteText := ""
	fsWriteStdin := false
	fsWriteCmd := &cobra.Command{
		Use:   "write <absolute-path> (--text text|--stdin)",
		Short: "Create or replace a remote file",
		Args:  exactArgs(1, "gs fs write /account/path --text text"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fsWriteStdin && fsWriteText != "" {
				return userError("invalid_args", "use --text or --stdin, not both", "Run gs fs write /account/path --text text.")
			}
			if !fsWriteStdin && fsWriteText == "" {
				return userError("invalid_args", "file content is required", "Pass --text text or --stdin.")
			}
			var data []byte
			if fsWriteStdin {
				var err error
				data, err = io.ReadAll(r.stdin())
				if err != nil {
					return err
				}
			} else {
				data = []byte(fsWriteText)
			}
			return r.runFileWrite(cmd.Context(), *opts, args[0], data)
		},
	}
	fsWriteCmd.Flags().StringVar(&fsWriteText, "text", fsWriteText, "file content")
	fsWriteCmd.Flags().BoolVar(&fsWriteStdin, "stdin", fsWriteStdin, "read file content from stdin")
	fsUploadRecursive := false
	fsUploadConcurrency := defaultUploadConcurrency()
	fsUploadCmd := &cobra.Command{
		Use:   "upload <local-path> <absolute-remote-path>",
		Short: "Upload a local file or directory into the home slice",
		Args:  exactArgs(2, "gs fs upload ./local /account/path [--recursive]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFSUpload(cmd.Context(), *opts, args[0], args[1], fsUploadOptions{
				Recursive:   fsUploadRecursive,
				Concurrency: fsUploadConcurrency,
			})
		},
	}
	fsUploadCmd.Flags().BoolVarP(&fsUploadRecursive, "recursive", "r", fsUploadRecursive, "upload a directory recursively")
	fsUploadCmd.Flags().IntVar(&fsUploadConcurrency, "concurrency", fsUploadConcurrency, "maximum concurrent file uploads")
	fsMvCmd := &cobra.Command{
		Use:   "mv <absolute-old-path> <absolute-new-path>",
		Short: "Rename or move a remote file or directory",
		Args:  exactArgs(2, "gs fs mv /account/old /account/new"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFileMove(cmd.Context(), *opts, args[0], args[1])
		},
	}
	fsRmCmd := &cobra.Command{
		Use:   "rm <absolute-path>",
		Short: "Delete a remote file or directory",
		Args:  exactArgs(1, "gs fs rm /account/path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFileRemove(cmd.Context(), *opts, args[0])
		},
	}
	fsCmd.AddCommand(fsLsCmd, fsCatCmd, fsMkdirCmd, fsTouchCmd, fsWriteCmd, fsUploadCmd, fsMvCmd, fsRmCmd)

	return fsCmd
}

// importCommand builds gs import, the Git history importer.
func (r Runner) importCommand(opts *commandOptions) *cobra.Command {
	importMountPath := ""
	importSlice := ""
	importMode := "shallow"
	importDeep := false
	importMaxCommits := 0
	importResume := true
	importRetries := defaultImportRetries
	importCmd := &cobra.Command{
		Use:   "import <source>",
		Short: "Import a Git repository under a mounted path",
		Args:  exactArgs(1, "gs import <source> --mount /acme/payment/vendor/repo [--slice payment|acme/payment] [--mode shallow|deep]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := importMode
			if importDeep {
				mode = "deep"
			}
			return r.runImportGitRepository(cmd.Context(), *opts, args[0], importMountPath, importSlice, mode, importMaxCommits, importResume, importRetries)
		},
	}
	importCmd.Flags().StringVar(&importMountPath, "mount", importMountPath, "absolute Gitslice path where the repository should be mounted")
	importCmd.Flags().StringVar(&importSlice, "slice", importSlice, "authoring slice; defaults to the mount path's account home slice (e.g. /nic/... -> nic:home)")
	importCmd.Flags().StringVar(&importMode, "mode", importMode, "import mode: shallow or deep")
	importCmd.Flags().BoolVar(&importDeep, "deep", importDeep, "import every reachable Git commit")
	importCmd.Flags().IntVar(&importMaxCommits, "max-commits", importMaxCommits, "maximum recent commits to import in deep mode")
	importCmd.Flags().BoolVar(&importResume, "resume", importResume, "resume previously completed commits for the same import")
	importCmd.Flags().IntVar(&importRetries, "retries", importRetries, "when the connection drops, reconnect and resume up to this many times in a row without progress (0 = never)")

	return importCmd
}

// shellCommand builds gs shell, the interactive server shell.
func (r Runner) shellCommand(opts *commandOptions) *cobra.Command {
	shellCommit := ""
	shellSlice := ""
	shellCmd := &cobra.Command{
		Use:   "shell",
		Short: "Browse server-side files",
		Args:  noArgs("gs shell [--slice slice|account:slice] [--commit commit-id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runShell(cmd.Context(), *opts, shellCommit, shellSlice)
		},
	}
	shellCmd.Flags().StringVar(&shellCommit, "commit", shellCommit, "native commit id to inspect; defaults to refs/global/main")
	shellCmd.Flags().StringVar(&shellSlice, "slice", shellSlice, "slice to attach, defaults to workspace slice or personal home slice")

	return shellCmd
}
