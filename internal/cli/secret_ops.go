package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/term"
)

func (r Runner) runSecretList(ctx context.Context, opts commandOptions, account string) error {
	var resp *corev1.ListSecretsResponse
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		if account, err = s.ResolveAccount(account); err != nil {
			return err
		}
		resp, err = s.Secret().ListSecrets(s.Ctx(), &corev1.ListSecretsRequest{Account: account})
		return secretError(err)
	}); err != nil {
		return err
	}
	out := make([]secretOutput, 0, len(resp.Secrets))
	for _, secret := range resp.Secrets {
		out = append(out, secretToOutput(secret))
	}
	if opts.JSONOutput() {
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
	if !model.ValidSliceSecretName(name) {
		return userError("invalid_secret_name", "secret name must match ^[A-Z_][A-Z0-9_]*$", "Use an environment-variable style name such as CI_TOKEN.")
	}
	var out secretOutput
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		if account, err = s.ResolveAccount(account); err != nil {
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
		secret, err := s.Secret().CreateSecret(s.Ctx(), &corev1.CreateSecretRequest{
			Account: account,
			Name:    name,
			Value:   value,
			Scope:   scope,
		})
		if err != nil {
			return secretError(err)
		}
		out = secretToOutput(secret)
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "created secret %s (%s) in %s for %s\n", out.Name, out.ID, out.Account, secretScopeLabel(out))
	}
	return nil
}

func (r Runner) runSecretUpdate(ctx context.Context, opts commandOptions, account, ref string, replaceValue bool, flags secretScopeFlags, add, remove []string) error {
	var out secretOutput
	if err := r.withSession(ctx, func(s *Session) error {
		id, err := resolveSecretRef(s, account, ref)
		if err != nil {
			return err
		}
		current, err := s.Secret().GetSecret(s.Ctx(), &corev1.GetSecretRequest{Id: id})
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
		secret, err := s.Secret().UpdateSecret(s.Ctx(), req)
		if err != nil {
			return secretError(err)
		}
		out = secretToOutput(secret)
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "updated secret %s (%s) in %s for %s\n", out.Name, out.ID, out.Account, secretScopeLabel(out))
	}
	return nil
}

func (r Runner) runSecretDelete(ctx context.Context, opts commandOptions, account, ref string) error {
	var id string
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		if id, err = resolveSecretRef(s, account, ref); err != nil {
			return err
		}
		_, err = s.Secret().DeleteSecret(s.Ctx(), &corev1.DeleteSecretRequest{Id: id})
		return secretError(err)
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, map[string]any{"deleted": id})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "deleted secret %s\n", id)
	}
	return nil
}

// resolveSecretRef turns a secret id (sec_...) or a secret's NAME in the
// account into its id. A NAME is unambiguous: an account has one secret of
// each name.
func resolveSecretRef(s *Session, account, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "sec_") {
		return ref, nil
	}
	account, err := s.ResolveAccount(account)
	if err != nil {
		return "", err
	}
	resp, err := s.Secret().ListSecrets(s.Ctx(), &corev1.ListSecretsRequest{Account: account})
	if err != nil {
		return "", secretError(err)
	}
	var ids []string
	for _, secret := range resp.GetSecrets() {
		if secret.GetName() == ref {
			ids = append(ids, secret.GetId())
		}
	}
	switch len(ids) {
	case 0:
		return "", userError("not_found", fmt.Sprintf("no secret %s in account %s", ref, account), "gs secret ls --account "+account+" lists them.")
	case 1:
		return ids[0], nil
	default:
		return "", userError("ambiguous", fmt.Sprintf("account %s has %d secrets named %s (%s)", account, len(ids), ref, strings.Join(ids, ", ")), "Pass the secret's id.")
	}
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
		raw, err = io.ReadAll(io.LimitReader(r.stdin(), model.MaxSecretValueBytes+2))
	}
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if value == "" {
		return "", userError("secret_value_required", "no value was given for "+name,
			`Pipe it in (printf %s "$TOKEN" | gs secret ...), or run the command in a terminal and type it.`)
	}
	if len(value) > model.MaxSecretValueBytes {
		return "", userError("invalid_argument", fmt.Sprintf("a secret value can be at most %d KiB", model.MaxSecretValueBytes>>10), "")
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
