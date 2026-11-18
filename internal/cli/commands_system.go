package cli

import (
	"github.com/spf13/cobra"
)

// configCommand builds gs config.
func (r Runner) configCommand(opts *commandOptions) *cobra.Command {
	configCmd := &cobra.Command{
		Use:     "config",
		Aliases: []string{"cfg"},
		Short:   "Manage local CLI configuration",
		RunE:    requireSubcommand("config"),
	}
	configListCmd := &cobra.Command{
		Use:   "list",
		Short: "List local CLI configuration",
		Args:  noArgs("gs config list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runConfigList(*opts)
		},
	}
	configGetCmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Get one local CLI configuration value",
		Args:  exactArgs(1, "gs config get <key>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runConfigGet(*opts, args[0])
		},
	}
	configSetCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one local CLI configuration value",
		Args:  exactArgs(2, "gs config set <key> <value>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runConfigSet(*opts, args[0], args[1])
		},
	}
	configCmd.AddCommand(configListCmd, configGetCmd, configSetCmd)

	return configCmd
}

// aliasCommand builds gs alias.
func (r Runner) aliasCommand(opts *commandOptions) *cobra.Command {
	aliasCmd := &cobra.Command{
		Use:   "alias",
		Short: "Manage local command aliases",
		RunE:  requireSubcommand("alias"),
	}
	aliasListCmd := &cobra.Command{
		Use:   "list",
		Short: "List local command aliases",
		Args:  noArgs("gs alias list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAliasList(*opts)
		},
	}
	aliasSetCmd := &cobra.Command{
		Use:   "set <name> <command>",
		Short: "Create or update a local command alias",
		Args:  exactArgs(2, "gs alias set <name> '<command>'"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAliasSet(*opts, args[0], args[1])
		},
	}
	aliasDeleteCmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a local command alias",
		Args:    exactArgs(1, "gs alias delete <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAliasDelete(*opts, args[0])
		},
	}
	aliasCmd.AddCommand(aliasListCmd, aliasSetCmd, aliasDeleteCmd)

	return aliasCmd
}

// rpcCommand builds gs rpc, which lists and calls the server's generated RPC methods.
func (r Runner) rpcCommand(opts *commandOptions) *cobra.Command {
	rpcCmd := &cobra.Command{
		Use:   "rpc",
		Short: "Call generated core RPCs for diagnostics",
		RunE:  requireSubcommand("rpc"),
	}
	rpcListCmd := &cobra.Command{
		Use:   "list",
		Short: "List generated core RPC methods",
		Args:  noArgs("gs rpc list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runRPCList(*opts)
		},
	}
	rpcCallServer := ""
	rpcCallRequest := "{}"
	rpcCallUnauthenticated := false
	rpcCallCmd := &cobra.Command{
		Use:   "call <service>/<method> [request-json]",
		Short: "Call a generated core RPC with a JSON request; a server-streaming RPC prints one JSON object per line",
		Args:  rangeArgs(1, 2, "gs rpc call <service>/<method> ['{...}' | --request '{...}']"),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := rpcCallRequest
			if len(args) == 2 {
				if cmd.Flags().Changed("request") {
					return userError("invalid_args", "pass the request either as an argument or with --request, not both", "")
				}
				request = args[1]
			}
			return r.runRPCCall(cmd.Context(), *opts, args[0], request, rpcCallServer, rpcCallUnauthenticated)
		},
	}
	rpcCallCmd.Flags().StringVar(&rpcCallRequest, "request", rpcCallRequest, "JSON request body")
	rpcCallCmd.Flags().StringVar(&rpcCallServer, "server", rpcCallServer, "server gRPC address override")
	rpcCallCmd.Flags().BoolVar(&rpcCallUnauthenticated, "unauthenticated", rpcCallUnauthenticated, "call without adding the saved bearer token")
	rpcCmd.AddCommand(rpcListCmd, rpcCallCmd)

	return rpcCmd
}

// browseCommand builds gs browse, which opens a slice, changeset or path in the web app.
func (r Runner) browseCommand(opts *commandOptions) *cobra.Command {
	browseWebURL := defaultWebURL()
	browsePrint := false
	browsePath := ""
	browseCmd := &cobra.Command{
		Use:   "browse [target]",
		Short: "Open the Gitslice web UI for a slice, path, or page",
		Long: `Open (or with --print, print) the web UI link for a target.

Targets:
  (none)              the current workspace's slice, or the web home page
  account:slice       a slice; add --path to open a file or directory in it
  /account/some/path  a source path; opened in the most specific slice you
                      can see that includes it (the account's home slice
                      otherwise)
  some/path           a path relative to the current workspace directory
  cs/<id>, slices/..., doc/..., claims, changesets, conversations
                      a web app page, as-is

For slice links it checks, when signed in, that the slice and path exist and
warns on stderr if the slice is private (only signed-in members can open it).`,
		Args: maxArgs(1, "gs browse [account:slice | /account/path | workspace/path | web-page] [--path /account/path] [--web-url url] [--print]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			return r.runBrowse(cmd.Context(), *opts, browseWebURL, target, browsePath, browsePrint)
		},
	}
	browseCmd.Flags().StringVar(&browsePath, "path", "", "file or directory to open inside the slice (absolute /account/... or workspace-relative)")
	browseCmd.Flags().StringVar(&browseWebURL, "web-url", browseWebURL, "web UI base URL")
	browseCmd.Flags().BoolVar(&browsePrint, "print", browsePrint, "print the URL instead of opening a browser")

	return browseCmd
}

// versionCommand builds gs version.
func (r Runner) versionCommand(opts *commandOptions) *cobra.Command {
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print CLI version information",
		Args:  noArgs("gs version [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runVersion(cmd.Context(), *opts)
		},
	}

	return versionCmd
}

// schemaCommand builds gs schema, which describes every command for agents.
func (r Runner) schemaCommand(opts *commandOptions) *cobra.Command {
	schemaCmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the machine-readable CLI schema",
		Args:  noArgs("gs schema"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSchema(*opts)
		},
	}

	return schemaCmd
}
