package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
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
	AllSlices bool     `json:"all_slices"`
	Slices    []string `json:"slices"`
	CreatedBy string   `json:"created_by,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// secretCommand manages an account's secrets: values passed to the CI checks
// of the slices each is scoped to (design/33_secrets.md).
func (r Runner) secretCommand(opts *commandOptions) *cobra.Command {
	secretCmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage the secrets an account passes to its slices' CI checks",
		Long: "A secret is a value Gitslice keeps encrypted and passes to the CI checks of the slices it is scoped to,\n" +
			"as an environment variable named after it. A secret belongs to an account and applies to some of its\n" +
			"slices (--slice) or to all of them (--all-slices). Values are read from stdin, or typed hidden in a\n" +
			"terminal, and are never shown again. Only the account's owners and admins manage its secrets.",
		RunE: requireSubcommand("secret"),
	}

	var listAccount string
	listCmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List an account's secrets and the slices they reach",
		Args:    noArgs("gs secret ls [--account <account>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSecretList(cmd.Context(), *opts, listAccount)
		},
	}
	listCmd.Flags().StringVar(&listAccount, "account", "", "the account (default: your active account)")

	var createAccount string
	var createSlices []string
	var createAllSlices bool
	createCmd := &cobra.Command{
		Use:   "create <NAME> (--slice <slice>... | --all-slices)",
		Short: "Create a secret, reading its value from stdin",
		Args:  exactArgs(1, "gs secret create <NAME> [--account <account>] (--slice <slice>... | --all-slices) < value"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if createAllSlices == (len(createSlices) > 0) {
				return userError("scope_required", "choose --slice (one or more) or --all-slices", "A secret reaches the CI checks of the slices it is scoped to.")
			}
			return r.runSecretCreate(cmd.Context(), *opts, createAccount, args[0], createSlices, createAllSlices)
		},
	}
	createCmd.Flags().StringVar(&createAccount, "account", "", "the account that owns it (default: your active account)")
	createCmd.Flags().StringArrayVar(&createSlices, "slice", nil, "a slice of the account it applies to (repeatable)")
	createCmd.Flags().BoolVar(&createAllSlices, "all-slices", false, "apply it to every slice of the account, including later ones")

	var updateValue, updateAllSlices bool
	var updateSlices, updateAdd, updateRemove []string
	updateCmd := &cobra.Command{
		Use:   "update <secret-id>",
		Short: "Replace a secret's value or change the slices it reaches",
		Args:  exactArgs(1, "gs secret update <secret-id> [--value] [--slice <slice>... | --add-slice <slice>... | --remove-slice <slice>... | --all-slices]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			scopes := 0
			for _, set := range []bool{len(updateSlices) > 0, len(updateAdd) > 0 || len(updateRemove) > 0, updateAllSlices} {
				if set {
					scopes++
				}
			}
			if scopes > 1 {
				return userError("invalid_argument", "use one of --slice, --add-slice/--remove-slice or --all-slices", "")
			}
			if scopes == 0 && !updateValue {
				return userError("invalid_argument", "nothing to change", "Pass --value to replace the value (read from stdin), or change the scope with --slice, --add-slice, --remove-slice or --all-slices.")
			}
			return r.runSecretUpdate(cmd.Context(), *opts, args[0], updateValue, updateSlices, updateAdd, updateRemove, updateAllSlices)
		},
	}
	updateCmd.Flags().BoolVar(&updateValue, "value", false, "replace the value, read from stdin")
	updateCmd.Flags().StringArrayVar(&updateSlices, "slice", nil, "scope it to exactly these slices (repeatable)")
	updateCmd.Flags().StringArrayVar(&updateAdd, "add-slice", nil, "add a slice to its scope (repeatable)")
	updateCmd.Flags().StringArrayVar(&updateRemove, "remove-slice", nil, "take a slice out of its scope (repeatable)")
	updateCmd.Flags().BoolVar(&updateAllSlices, "all-slices", false, "apply it to every slice of the account")

	deleteCmd := &cobra.Command{
		Use:     "rm <secret-id>",
		Aliases: []string{"delete", "remove"},
		Short:   "Delete a secret from every slice it reaches",
		Args:    exactArgs(1, "gs secret rm <secret-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSecretDelete(cmd.Context(), *opts, args[0])
		},
	}

	secretCmd.AddCommand(listCmd, createCmd, updateCmd, deleteCmd)
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

func (r Runner) runSecretCreate(ctx context.Context, opts commandOptions, account, name string, slices []string, allSlices bool) error {
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
	scope := &corev1.SecretScope{AllSlices: allSlices}
	for _, slice := range slices {
		ref, err := secretSliceRef(account, slice)
		if err != nil {
			return err
		}
		scope.Slices = append(scope.Slices, ref)
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

func (r Runner) runSecretUpdate(ctx context.Context, opts commandOptions, id string, replaceValue bool, slices, add, remove []string, allSlices bool) error {
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
	case allSlices:
		req.Scope = &corev1.SecretScope{AllSlices: true}
	case len(slices) > 0:
		req.Scope = &corev1.SecretScope{}
		for _, slice := range slices {
			ref, err := secretSliceRef(current.Account, slice)
			if err != nil {
				return err
			}
			req.Scope.Slices = append(req.Scope.Slices, ref)
		}
	case len(add) > 0 || len(remove) > 0:
		if current.GetScope().GetAllSlices() {
			return userError("invalid_argument", current.Name+" applies to every slice of "+current.Account, "Scope it to some slices with --slice instead.")
		}
		names := map[string]bool{}
		var order []string
		for _, ref := range current.GetScope().GetSlices() {
			names[ref.Slice] = true
			order = append(order, ref.Slice)
		}
		for _, slice := range add {
			ref, err := secretSliceRef(current.Account, slice)
			if err != nil {
				return err
			}
			if ref.Account != current.Account {
				return userError("invalid_argument", "slice "+ref.Account+"/"+ref.Slice+" is not in account "+current.Account, "A secret applies only to its own account's slices.")
			}
			if !names[ref.Slice] {
				names[ref.Slice] = true
				order = append(order, ref.Slice)
			}
		}
		for _, slice := range remove {
			ref, err := secretSliceRef(current.Account, slice)
			if err != nil {
				return err
			}
			delete(names, ref.Slice)
		}
		req.Scope = &corev1.SecretScope{}
		for _, slice := range order {
			if names[slice] {
				req.Scope.Slices = append(req.Scope.Slices, &corev1.SliceRef{Account: current.Account, Slice: slice})
			}
		}
		if len(req.Scope.Slices) == 0 {
			return userError("invalid_argument", "that would leave "+current.Name+" with no slices", "Delete it with gs secret rm "+current.Id+" instead.")
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
	out := secretOutput{
		ID:        secret.GetId(),
		Account:   secret.GetAccount(),
		Name:      secret.GetName(),
		AllSlices: secret.GetScope().GetAllSlices(),
		Slices:    []string{},
		CreatedBy: secret.GetCreatedBy(),
		UpdatedBy: secret.GetUpdatedBy(),
		CreatedAt: secret.GetCreatedAt(),
		UpdatedAt: secret.GetUpdatedAt(),
	}
	for _, ref := range secret.GetScope().GetSlices() {
		out.Slices = append(out.Slices, ref.GetSlice())
	}
	return out
}

func secretScopeLabel(secret secretOutput) string {
	switch {
	case secret.AllSlices:
		return "all slices"
	case len(secret.Slices) == 0:
		return "no slices"
	}
	return strings.Join(secret.Slices, ",")
}

func secretError(err error) error {
	message := grpcstatus.Convert(err).Message()
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		return userError("invalid_argument", message, "")
	case codes.FailedPrecondition:
		return userError("failed_precondition", message, "gs secret ls lists the account's secrets and the slices each reaches.")
	case codes.PermissionDenied:
		return userError("permission_denied", message, "Secrets are managed by the account's owners and admins.")
	case codes.NotFound:
		return userError("not_found", message, "")
	case codes.Unimplemented:
		return userError("unsupported", "this server does not support account secrets", "Upgrade the server, or use gs slice secret.")
	}
	return err
}
