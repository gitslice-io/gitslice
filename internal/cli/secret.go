package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type secretOutput struct {
	ID        string   `json:"id"`
	Account   string   `json:"account"`
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
	CreatedBy string   `json:"created_by,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

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
		Use:   "create <NAME> (--path <folder>... | --slice <slice>...)",
		Short: "Create a secret, reading its value from stdin",
		Args:  exactArgs(1, "gs secret create <NAME> [--account <account>] (--path <folder>... | --slice <slice>...) < value"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !create.set() {
				return userError("scope_required", "choose the folders it applies to with --path (or --slice for a slice's folders)", "A secret reaches the CI checks defined in its folders and below them; --path /<account> applies it to the whole account.")
			}
			return r.runSecretCreate(cmd.Context(), *opts, createAccount, args[0], create)
		},
	}
	createCmd.Flags().StringVar(&createAccount, "account", "", "the account that owns it (default: your active account)")
	create.register(createCmd, "a folder it applies to, with everything below it, such as /acme/services (repeatable)", "the folders of a slice of the account (repeatable)")

	var updateValue bool
	var update secretScopeFlags
	var updateAdd, updateRemove []string
	updateCmd := &cobra.Command{
		Use:   "update <secret-id>",
		Short: "Replace a secret's value or change the folders it applies to",
		Args:  exactArgs(1, "gs secret update <secret-id> [--value] [--path <folder>... | --slice <slice>... | --add-path <folder>... | --remove-path <folder>...]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if update.set() && (len(updateAdd) > 0 || len(updateRemove) > 0) {
				return userError("invalid_argument", "use --path/--slice, or --add-path/--remove-path, not both", "")
			}
			if !update.set() && len(updateAdd) == 0 && len(updateRemove) == 0 && !updateValue {
				return userError("invalid_argument", "nothing to change", "Pass --value to replace the value (read from stdin), or change the folders with --path, --slice, --add-path or --remove-path.")
			}
			return r.runSecretUpdate(cmd.Context(), *opts, args[0], updateValue, update, updateAdd, updateRemove)
		},
	}
	updateCmd.Flags().BoolVar(&updateValue, "value", false, "replace the value, read from stdin")
	update.register(updateCmd, "apply it to exactly these folders (repeatable)", "apply it to exactly these slices' folders (repeatable)")
	updateCmd.Flags().StringArrayVar(&updateAdd, "add-path", nil, "add a folder (repeatable)")
	updateCmd.Flags().StringArrayVar(&updateRemove, "remove-path", nil, "remove a folder (repeatable)")

	deleteCmd := &cobra.Command{
		Use:     "rm <secret-id>",
		Aliases: []string{"delete", "remove"},
		Short:   "Delete a secret from every slice it reaches",
		Args:    exactArgs(1, "gs secret rm <secret-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSecretDelete(cmd.Context(), *opts, args[0])
		},
	}

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

func (r Runner) secretAccount(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, account string) (string, error) {
	if strings.TrimSpace(account) != "" {
		return normalizeCLISlug(account, "account")
	}
	return r.defaultSliceAccount(ctx, cfg, conn)
}

func (r Runner) runSecretList(ctx context.Context, opts commandOptions, account string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if account, err = r.secretAccount(callCtx, cfg, conn, account); err != nil {
		return err
	}
	resp, err := corev1.NewSecretServiceClient(conn).ListSecrets(callCtx, &corev1.ListSecretsRequest{Account: account})
	if err != nil {
		return secretError(err)
	}
	out := make([]secretOutput, 0, len(resp.Secrets))
	for _, secret := range resp.Secrets {
		out = append(out, secretToOutput(secret))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"secrets": out})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintf(r.Stdout, "no secrets in %s\n", account)
		return nil
	}
	for _, secret := range out {
		updated := secret.UpdatedAt
		if secret.UpdatedBy != "" {
			updated += " by " + secret.UpdatedBy
		}
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%s\n", secret.ID, secret.Name, secretScopeLabel(secret), updated)
	}
	return nil
}

func (r Runner) runSecretCreate(ctx context.Context, opts commandOptions, account, name string, flags secretScopeFlags) error {
	name = strings.TrimSpace(name)
	if !storage.ValidSliceSecretName(name) {
		return userError("invalid_secret_name", "secret name must match ^[A-Z_][A-Z0-9_]*$", "Use an environment-variable style name such as CI_TOKEN.")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if account, err = r.secretAccount(callCtx, cfg, conn, account); err != nil {
		return err
	}
	scope, err := flags.scope(account)
	if err != nil {
		return err
	}
	value, err := r.readSecretValue(name)
	if err != nil {
		return err
	}
	secret, err := corev1.NewSecretServiceClient(conn).CreateSecret(callCtx, &corev1.CreateSecretRequest{
		Account: account,
		Name:    name,
		Value:   value,
		Scope:   scope,
	})
	if err != nil {
		return secretError(err)
	}
	out := secretToOutput(secret)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "created secret %s (%s) in %s for %s\n", out.Name, out.ID, out.Account, secretScopeLabel(out))
	}
	return nil
}

func (r Runner) runSecretUpdate(ctx context.Context, opts commandOptions, id string, replaceValue bool, flags secretScopeFlags, add, remove []string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewSecretServiceClient(conn)
	current, err := client.GetSecret(callCtx, &corev1.GetSecretRequest{Id: strings.TrimSpace(id)})
	if err != nil {
		return secretError(err)
	}
	req := &corev1.UpdateSecretRequest{Id: current.Id}
	switch {
	case flags.set():
		if req.Scope, err = flags.scope(current.Account); err != nil {
			return err
		}
	case len(add) > 0 || len(remove) > 0:
		kept := map[string]bool{}
		var order []string
		for _, p := range current.GetScope().GetPaths() {
			kept[p] = true
			order = append(order, p)
		}
		for _, raw := range add {
			p, err := secretPathFlag(raw)
			if err != nil {
				return err
			}
			if !kept[p] {
				kept[p] = true
				order = append(order, p)
			}
		}
		for _, raw := range remove {
			p, err := secretPathFlag(raw)
			if err != nil {
				return err
			}
			delete(kept, p)
		}
		req.Scope = &corev1.SecretScope{}
		for _, p := range order {
			if kept[p] {
				req.Scope.Paths = append(req.Scope.Paths, p)
			}
		}
		if len(req.Scope.Paths) == 0 {
			return userError("invalid_argument", "that would leave "+current.Name+" with no folders", "Delete it with gs secret rm "+current.Id+" instead.")
		}
	}
	if replaceValue {
		value, err := r.readSecretValue(current.Name)
		if err != nil {
			return err
		}
		req.Value = &value
	}
	secret, err := client.UpdateSecret(callCtx, req)
	if err != nil {
		return secretError(err)
	}
	out := secretToOutput(secret)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "updated secret %s (%s) in %s for %s\n", out.Name, out.ID, out.Account, secretScopeLabel(out))
	}
	return nil
}

func (r Runner) runSecretDelete(ctx context.Context, opts commandOptions, id string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	id = strings.TrimSpace(id)
	if _, err := corev1.NewSecretServiceClient(conn).DeleteSecret(callCtx, &corev1.DeleteSecretRequest{Id: id}); err != nil {
		return secretError(err)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"deleted": id})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "deleted secret %s\n", id)
	}
	return nil
}

// readSecretValue reads a secret's value: typed hidden when stdin is a
// terminal, else the whole of stdin without its final newline.
func (r Runner) readSecretValue(name string) (string, error) {
	var raw []byte
	var err error
	if isTerminal(r.stdin()) {
		fmt.Fprintf(r.stderr(), "Value of %s (input is hidden): ", name)
		raw, err = term.ReadPassword(int(r.stdin().(*os.File).Fd()))
		fmt.Fprintln(r.stderr())
	} else {
		raw, err = io.ReadAll(io.LimitReader(r.stdin(), storage.MaxSecretValueBytes+2))
	}
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if value == "" {
		return "", userError("secret_value_required", "no value was given for "+name,
			`Pipe it in (printf %s "$TOKEN" | gs secret ...), or run the command in a terminal and type it.`)
	}
	if len(value) > storage.MaxSecretValueBytes {
		return "", userError("invalid_argument", fmt.Sprintf("a secret value can be at most %d KiB", storage.MaxSecretValueBytes>>10), "")
	}
	return value, nil
}

// secretSliceRef reads a --slice value: a slice of the account, or
// account/slice.
func secretSliceRef(account, value string) (*corev1.SliceRef, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, ":/") {
		return parseSliceRef(value)
	}
	slug, err := normalizeCLISlug(value, "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

func secretToOutput(secret *corev1.Secret) secretOutput {
	return secretOutput{
		ID:        secret.GetId(),
		Account:   secret.GetAccount(),
		Name:      secret.GetName(),
		Paths:     append([]string{}, secret.GetScope().GetPaths()...),
		CreatedBy: secret.GetCreatedBy(),
		UpdatedBy: secret.GetUpdatedBy(),
		CreatedAt: secret.GetCreatedAt(),
		UpdatedAt: secret.GetUpdatedAt(),
	}
}

func secretScopeLabel(secret secretOutput) string {
	if len(secret.Paths) == 0 {
		return "no folders"
	}
	return strings.Join(secret.Paths, ",")
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

func secretError(err error) error {
	message := grpcstatus.Convert(err).Message()
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		return userError("invalid_argument", message, "")
	case codes.FailedPrecondition:
		return userError("failed_precondition", message, "gs secret ls lists the account's secrets and the folders each applies to.")
	case codes.PermissionDenied:
		return userError("permission_denied", message, "Secrets are managed by the account's owners and admins.")
	case codes.NotFound:
		return userError("not_found", message, "")
	case codes.Unimplemented:
		return userError("unsupported", "this server does not support account secrets", "Upgrade the server, or use gs slice secret.")
	}
	return err
}
