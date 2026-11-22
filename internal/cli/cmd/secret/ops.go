package secret

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/term"
)

func runList(app clibase.App, ctx context.Context, opts clibase.Options, account string) error {
	var resp *corev1.ListSecretsResponse
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		var err error
		if account, err = s.ResolveAccount(account); err != nil {
			return err
		}
		resp, err = s.Secret().ListSecrets(s.Ctx(), &corev1.ListSecretsRequest{Account: account})
		return secretError(err)
	}); err != nil {
		return err
	}
	out := make([]output, 0, len(resp.Secrets))
	for _, s := range resp.Secrets {
		out = append(out, toOutput(s))
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, map[string]any{"secrets": out})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintf(app.Stdout(), "no secrets in %s\n", account)
		return nil
	}
	for _, s := range out {
		updated := s.UpdatedAt
		if s.UpdatedBy != "" {
			updated += " by " + s.UpdatedBy
		}
		fmt.Fprintf(app.Stdout(), "%s\t%s\t%s\t%s\n", s.ID, s.Name, scopeLabel(s), updated)
	}
	return nil
}

func runCreate(app clibase.App, ctx context.Context, opts clibase.Options, account, name string, flags scopeFlags) error {
	name = strings.TrimSpace(name)
	if !model.ValidSliceSecretName(name) {
		return clibase.UserError("invalid_secret_name", "secret name must match ^[A-Z_][A-Z0-9_]*$", "Use an environment-variable style name such as CI_TOKEN.")
	}
	var out output
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		var err error
		if account, err = s.ResolveAccount(account); err != nil {
			return err
		}
		scope, err := flags.scope(account)
		if err != nil {
			return err
		}
		value, err := readSecretValue(app, name)
		if err != nil {
			return err
		}
		sec, err := s.Secret().CreateSecret(s.Ctx(), &corev1.CreateSecretRequest{
			Account: account,
			Name:    name,
			Value:   value,
			Scope:   scope,
		})
		if err != nil {
			return secretError(err)
		}
		out = toOutput(sec)
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stdout(), "created secret %s (%s) in %s for %s\n", out.Name, out.ID, out.Account, scopeLabel(out))
	}
	return nil
}

func runUpdate(app clibase.App, ctx context.Context, opts clibase.Options, account, ref string, replaceValue bool, flags scopeFlags, add, remove []string) error {
	var out output
	if err := app.WithSession(ctx, func(s clibase.Session) error {
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
				return clibase.UserError("invalid_argument", "that would leave "+current.Name+" with no folders", "Delete it with gs secret rm "+current.Id+" instead.")
			}
		}
		if replaceValue {
			value, err := readSecretValue(app, current.Name)
			if err != nil {
				return err
			}
			req.Value = &value
		}
		sec, err := s.Secret().UpdateSecret(s.Ctx(), req)
		if err != nil {
			return secretError(err)
		}
		out = toOutput(sec)
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stdout(), "updated secret %s (%s) in %s for %s\n", out.Name, out.ID, out.Account, scopeLabel(out))
	}
	return nil
}

func runDelete(app clibase.App, ctx context.Context, opts clibase.Options, account, ref string) error {
	var id string
	if err := app.WithSession(ctx, func(s clibase.Session) error {
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
		return app.WriteJSONOutput(opts, map[string]any{"deleted": id})
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stdout(), "deleted secret %s\n", id)
	}
	return nil
}

func resolveSecretRef(s clibase.Session, account, ref string) (string, error) {
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
	for _, sec := range resp.GetSecrets() {
		if sec.GetName() == ref {
			ids = append(ids, sec.GetId())
		}
	}
	switch len(ids) {
	case 0:
		return "", clibase.UserError("not_found", fmt.Sprintf("no secret %s in account %s", ref, account), "gs secret ls --account "+account+" lists them.")
	case 1:
		return ids[0], nil
	default:
		return "", clibase.UserError("ambiguous", fmt.Sprintf("account %s has %d secrets named %s (%s)", account, len(ids), ref, strings.Join(ids, ", ")), "Pass the secret's id.")
	}
}

// ReadValue reads a secret value from stdin (masked when stdin is a terminal).
func ReadValue(app clibase.App, name string) (string, error) {
	return readSecretValue(app, name)
}

func readSecretValue(app clibase.App, name string) (string, error) {
	var raw []byte
	var err error
	if clibase.IsTerminal(app.Stdin()) {
		fmt.Fprintf(app.Stderr(), "Value of %s (input is hidden): ", name)
		raw, err = term.ReadPassword(int(app.Stdin().(*os.File).Fd()))
		fmt.Fprintln(app.Stderr())
	} else {
		raw, err = io.ReadAll(io.LimitReader(app.Stdin(), model.MaxSecretValueBytes+2))
	}
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if value == "" {
		return "", clibase.UserError("secret_value_required", "no value was given for "+name,
			`Pipe it in (printf %s "$TOKEN" | gs secret ...), or run the command in a terminal and type it.`)
	}
	if len(value) > model.MaxSecretValueBytes {
		return "", clibase.UserError("invalid_argument", fmt.Sprintf("a secret value can be at most %d KiB", model.MaxSecretValueBytes>>10), "")
	}
	return value, nil
}

func secretSliceRef(account, value string) (*corev1.SliceRef, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, ":/") {
		return clibase.ParseSliceRef(value)
	}
	slug, err := clibase.NormalizeSlug(value, "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}
