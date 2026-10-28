package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// secretPlaceholderPrefix marks a template value that gs secret env fills
// from a Gitslice secret: NAME=gitslice-secret:SECRET_NAME.
const secretPlaceholderPrefix = "gitslice-secret:"

var envAssignment = regexp.MustCompile(`^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// envTemplateLine is one line of an env template: kept as it is, or a
// variable whose value comes from the secret named secret.
type envTemplateLine struct {
	text   string
	prefix string
	key    string
	secret string
}

// parseEnvTemplate reads an env file whose values may be placeholders, and
// returns its lines and the secret names they ask for, sorted.
func parseEnvTemplate(content string) ([]envTemplateLine, []string, error) {
	var lines []envTemplateLine
	wanted := map[string]bool{}
	for i, text := range strings.Split(content, "\n") {
		line := envTemplateLine{text: text}
		if m := envAssignment.FindStringSubmatch(text); m != nil {
			value := strings.TrimSpace(m[3])
			if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
				value = value[1 : len(value)-1]
			}
			if strings.HasPrefix(value, secretPlaceholderPrefix) {
				name := strings.TrimPrefix(value, secretPlaceholderPrefix)
				if !storage.ValidSliceSecretName(name) {
					return nil, nil, userError("invalid_template", fmt.Sprintf("line %d: %q is not a secret name", i+1, name), "Write placeholders as NAME="+secretPlaceholderPrefix+"SECRET_NAME, with SECRET_NAME like CI_TOKEN.")
				}
				line.prefix, line.key, line.secret = m[1], m[2], name
				wanted[name] = true
			}
		}
		lines = append(lines, line)
	}
	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	sort.Strings(names)
	return lines, names, nil
}

// renderEnvTemplate fills the placeholders with values, quoted for a shell
// that sources the file.
func renderEnvTemplate(lines []envTemplateLine, values map[string]string) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line.secret == "" {
			out = append(out, line.text)
			continue
		}
		out = append(out, line.prefix+line.key+"="+shellQuoteEnvValue(values[line.secret]))
	}
	return strings.Join(out, "\n")
}

var envSafeValue = regexp.MustCompile(`^[A-Za-z0-9_./:@+,=-]*$`)

func shellQuoteEnvValue(value string) string {
	if envSafeValue.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (r Runner) runSecretEnv(ctx context.Context, opts commandOptions, templatePath, slice, output string, force bool) error {
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return userError("template_unreadable", err.Error(), "")
	}
	lines, names, err := parseEnvTemplate(string(content))
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return userError("invalid_template", templatePath+" has no "+secretPlaceholderPrefix+" placeholders", "Write a secret's place as NAME="+secretPlaceholderPrefix+"SECRET_NAME.")
	}
	toStdout := output == "" || output == "-"
	if toStdout && isTerminal(r.stdout()) {
		return userError("refusing_terminal", "refusing to print secret values to a terminal", "Write them to a file with -o .env.prod, or redirect stdout.")
	}
	if !toStdout && !force {
		if _, err := os.Stat(output); err == nil {
			return userError("output_exists", output+" already exists", "Pass --force to replace it.")
		}
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.tagSliceRef(callCtx, cfg, conn, slice)
	if err != nil {
		return err
	}
	resp, err := corev1.NewSecretServiceClient(conn).RevealSliceSecrets(callCtx, &corev1.RevealSliceSecretsRequest{Slice: ref, Names: names})
	if err != nil {
		return secretError(err)
	}
	if len(resp.Missing) > 0 {
		return userError("secret_missing", fmt.Sprintf("no secret of %s/%s is named %s", ref.Account, ref.Slice, strings.Join(resp.Missing, ", ")),
			"Create it with gs secret create NAME --slice "+ref.Slice+" < value, or fix the template.")
	}
	rendered := renderEnvTemplate(lines, resp.Values)
	if toStdout {
		_, err := fmt.Fprint(r.Stdout, rendered)
		return err
	}
	if err := writePrivateFile(output, []byte(rendered)); err != nil {
		return err
	}
	if !opts.Quiet {
		fmt.Fprintf(r.stderr(), "wrote %s (mode 600) with %d secret(s) of %s/%s\n", output, len(names), ref.Account, ref.Slice)
	}
	return nil
}

// writePrivateFile writes data readable by its owner only, replacing path
// in one step.
func writePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
