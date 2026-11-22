package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

// secretCommand manages an account's secrets: values passed to the CI checks
// defined in the folders each is scoped to (design/33_secrets.md).
func (r Runner) secretCommand(opts *commandOptions) *cobra.Command {
	secretCmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage the secrets an account passes to CI checks, by folder",
		Long: "A secret is a value Gitslice keeps encrypted and passes, as an environment variable named after it, to the\n" +
			"CI checks defined in the folders it applies to and everything below them (--path /acme/services, or\n" +
			"--path /acme for the whole account; --slice is short for a slice's folders). Where two secrets of one name\n" +
			"apply, the one on the closer folder wins. Values are read from stdin, or typed hidden in a terminal. Only\n" +
			"the account's owners and admins manage its secrets, and only they can read values back, into a local env\n" +
			"file (gs secret env). See gs help secrets.",
		RunE: requireSubcommand("secret"),
	}
	markSchemaGroup(secretCmd)

	var listAccount string
	listCmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List an account's secrets and the folders they apply to",
		Args:    noArgs("gs secret ls [--account <account>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSecretList(cmd.Context(), *opts, listAccount)
		},
	}
	listCmd.Flags().StringVar(&listAccount, "account", "", "the account (default: your active account)")

	var createAccount string
	var create secretScopeFlags
	createCmd := &cobra.Command{
		Use:   "create <NAME> [--path <folder>...]",
		Short: "Create a folder-scoped secret, reading its value from stdin",
		Args:  exactArgs(1, "gs secret create <NAME> [--account <account>] [--path <folder>...] < value"),
		RunE: func(cmd *cobra.Command, args []string) error {
			scopeFlags := create
			targetAccount := createAccount
			if !scopeFlags.set() {
				folder, err := r.triggerFolder("")
				if err != nil {
					return userError("scope_required", "choose the folders it applies to with --path (or run inside a workspace folder)", "A secret reaches the CI checks and triggers defined in its folders and below them; --path /<account> applies it to the whole account.")
				}
				scopeFlags.paths = []string{folder}
				if strings.TrimSpace(targetAccount) == "" {
					if parts := strings.Split(strings.Trim(folder, "/"), "/"); len(parts) > 0 && parts[0] != "" {
						targetAccount = parts[0]
					}
				}
			}
			return r.runSecretCreate(cmd.Context(), *opts, targetAccount, args[0], scopeFlags)
		},
	}
	createCmd.Flags().StringVar(&createAccount, "account", "", "the account that owns it (default: inferred from folder or your active account)")
	create.register(createCmd, "a folder it applies to, with everything below it, such as /acme/services (default: current workspace folder; repeatable)", "deprecated: prefer --path; targets the folders of a slice (repeatable)")

	var updateValue bool
	var updateAccount string
	var update secretScopeFlags
	var updateAdd, updateRemove []string
	updateCmd := &cobra.Command{
		Use:   "update <secret-id|NAME>",
		Short: "Replace a secret's value or change the folders it applies to",
		Args:  exactArgs(1, "gs secret update <secret-id|NAME> [--account <account>] [--value] [--path <folder>... | --slice <slice>... | --add-path <folder>... | --remove-path <folder>...]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if update.set() && (len(updateAdd) > 0 || len(updateRemove) > 0) {
				return userError("invalid_argument", "use --path/--slice, or --add-path/--remove-path, not both", "")
			}
			if !update.set() && len(updateAdd) == 0 && len(updateRemove) == 0 && !updateValue {
				return userError("invalid_argument", "nothing to change", "Pass --value to replace the value (read from stdin), or change the folders with --path, --slice, --add-path or --remove-path.")
			}
			return r.runSecretUpdate(cmd.Context(), *opts, updateAccount, args[0], updateValue, update, updateAdd, updateRemove)
		},
	}
	updateCmd.Flags().StringVar(&updateAccount, "account", "", "the account of a secret given by NAME (default: your active account)")
	updateCmd.Flags().BoolVar(&updateValue, "value", false, "replace the value, read from stdin")
	update.register(updateCmd, "apply it to exactly these folders (repeatable)", "apply it to exactly these slices' folders (repeatable)")
	updateCmd.Flags().StringArrayVar(&updateAdd, "add-path", nil, "add a folder (repeatable)")
	updateCmd.Flags().StringArrayVar(&updateRemove, "remove-path", nil, "remove a folder (repeatable)")

	var deleteAccount string
	deleteCmd := &cobra.Command{
		Use:     "rm <secret-id|NAME>",
		Aliases: []string{"delete", "remove"},
		Short:   "Delete a secret from every folder it applies to",
		Args:    exactArgs(1, "gs secret rm <secret-id|NAME> [--account <account>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSecretDelete(cmd.Context(), *opts, deleteAccount, args[0])
		},
	}
	deleteCmd.Flags().StringVar(&deleteAccount, "account", "", "the account of a secret given by NAME (default: your active account)")

	var envSlice, envPath, envOutput string
	var envForce bool
	envCmd := &cobra.Command{
		Use:   "env <template>",
		Short: "Fill an env file's gitslice-secret: placeholders with secret values (account owners and admins)",
		Long: "Reads an env template, such as env.prod.template, and writes it with every NAME=gitslice-secret:SECRET_NAME\n" +
			"value replaced by the value of the secret that applies at the template's folder in the workspace (or at\n" +
			"--path; --slice uses a slice's folders). Only the owners and admins of the account may read values; each\n" +
			"read is logged. The output is written with mode 600, and gs refuses to print values to a terminal.",
		Args: exactArgs(1, "gs secret env <template> [--path <folder> | --slice <slice>] -o <file> [--force]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if envSlice != "" && envPath != "" {
				return userError("invalid_argument", "use --path or --slice, not both", "")
			}
			return r.runSecretEnv(cmd.Context(), *opts, args[0], envPath, envSlice, envOutput, envForce)
		},
	}
	envCmd.Flags().StringVar(&envPath, "path", "", "the folder whose secrets fill the template, such as /acme/web (default: the template's folder in the workspace)")
	envCmd.Flags().StringVar(&envSlice, "slice", "", "fill it with the secrets at a slice's folders instead")
	envCmd.Flags().StringVarP(&envOutput, "output", "o", "", "file to write (mode 600); - or empty for stdout, when it is not a terminal")
	envCmd.Flags().BoolVar(&envForce, "force", false, "replace the output file if it exists")

	secretCmd.AddCommand(listCmd, createCmd, updateCmd, deleteCmd, envCmd)
	return secretCmd
}

// secretScopeFlags are the flags that choose a secret's folders.
type secretScopeFlags struct {
	paths     []string
	slices    []string
	allSlices bool
}

func (f *secretScopeFlags) register(cmd *cobra.Command, pathUsage, sliceUsage string) {
	cmd.Flags().StringArrayVar(&f.paths, "path", nil, pathUsage)
	cmd.Flags().StringArrayVar(&f.slices, "slice", nil, sliceUsage)
	// Before secrets moved to folders this meant every slice of the account:
	// the account root now.
	cmd.Flags().BoolVar(&f.allSlices, "all-slices", false, "apply it to the whole account (same as --path /<account>)")
	_ = cmd.Flags().MarkHidden("all-slices")
}

func (f secretScopeFlags) set() bool {
	return len(f.paths) > 0 || len(f.slices) > 0 || f.allSlices
}

// scope sends the folders as given; the server turns slices into their
// included paths and all_slices into the account root.
func (f secretScopeFlags) scope(account string) (*corev1.SecretScope, error) {
	scope := &corev1.SecretScope{AllSlices: f.allSlices}
	for _, raw := range f.paths {
		p, err := secretPathFlag(raw)
		if err != nil {
			return nil, err
		}
		scope.Paths = append(scope.Paths, p)
	}
	for _, slice := range f.slices {
		ref, err := secretSliceRef(account, slice)
		if err != nil {
			return nil, err
		}
		scope.Slices = append(scope.Slices, ref)
	}
	return scope, nil
}

// secretPathFlag reads a --path value: an absolute folder such as
// /acme/services, made canonical.
func secretPathFlag(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "/") {
		return "", userError("invalid_argument", "--path "+raw+" must be absolute, starting with /<account>", "For example --path /acme/services, or --path /acme for the whole account.")
	}
	p, err := paths.CanonicalPrefix(raw)
	if err != nil {
		return "", userError("invalid_argument", "--path "+raw+": "+err.Error(), "")
	}
	return p, nil
}

var secretErrorHints = domainErrorOverrides{
	codes.FailedPrecondition: {
		Hint: "gs secret ls lists the account's secrets and the folders each applies to.",
	},
	codes.PermissionDenied: {
		Hint: "Secrets are managed by the account's owners and admins.",
	},
	codes.Unimplemented: {
		Message: "this server does not support account secrets",
		Hint:    "Upgrade the server, or use gs slice secret.",
	},
}

func secretError(err error) error {
	return withDomainErrorHints(err, secretErrorHints)
}
