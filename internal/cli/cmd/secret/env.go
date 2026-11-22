package secret

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/workspace"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const secretPlaceholderPrefix = "gitslice-secret:"

var envAssignment = regexp.MustCompile(`^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

type envTemplateLine struct {
	text   string
	prefix string
	key    string
	secret string
}

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
				if !model.ValidSliceSecretName(name) {
					return nil, nil, clibase.UserError("invalid_template", fmt.Sprintf("line %d: %q is not a secret name", i+1, name), "Write placeholders as NAME="+secretPlaceholderPrefix+"SECRET_NAME, with SECRET_NAME like CI_TOKEN.")
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

func runEnv(app clibase.App, ctx context.Context, opts clibase.Options, templatePath, folder, slice, output string, force bool) error {
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return clibase.UserError("template_unreadable", err.Error(), "")
	}
	lines, names, err := parseEnvTemplate(string(content))
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return clibase.UserError("invalid_template", templatePath+" has no "+secretPlaceholderPrefix+" placeholders", "Write a secret's place as NAME="+secretPlaceholderPrefix+"SECRET_NAME.")
	}
	toStdout := output == "" || output == "-"
	if toStdout && clibase.IsTerminal(app.Stdout()) {
		return clibase.UserError("refusing_terminal", "refusing to print secret values to a terminal", "Write them to a file with -o .env.prod, or redirect stdout.")
	}
	if !toStdout && !force {
		if _, err := os.Stat(output); err == nil {
			return clibase.UserError("output_exists", output+" already exists", "Pass --force to replace it.")
		}
	}
	if slice == "" {
		if folder, err = secretEnvFolder(templatePath, folder); err != nil {
			return err
		}
	}
	var values map[string]string
	var missing []string
	where, hint := folder, "Create it with gs secret create NAME --path "+folder+" < value, or fix the template."
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		if slice != "" {
			ref, err := s.ResolveSliceRef(slice, true)
			if err != nil {
				return err
			}
			resp, err := s.Secret().RevealSliceSecrets(s.Ctx(), &corev1.RevealSliceSecretsRequest{Slice: ref, Names: names})
			if err != nil {
				return secretError(err)
			}
			values, missing = resp.Values, resp.Missing
			where, hint = ref.Account+"/"+ref.Slice, "Create it with gs secret create NAME --slice "+ref.Slice+" < value, or fix the template."
			return nil
		}
		resp, err := s.Secret().RevealSecrets(s.Ctx(), &corev1.RevealSecretsRequest{Path: folder, Names: names})
		if err != nil {
			return secretError(err)
		}
		values, missing = resp.Values, resp.Missing
		return nil
	}); err != nil {
		return err
	}
	if len(missing) > 0 {
		return clibase.UserError("secret_missing", fmt.Sprintf("no secret at %s is named %s", where, strings.Join(missing, ", ")), hint)
	}
	rendered := renderEnvTemplate(lines, values)
	if toStdout {
		_, err := fmt.Fprint(app.Stdout(), rendered)
		return err
	}
	if err := writePrivateFile(output, []byte(rendered)); err != nil {
		return err
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stderr(), "wrote %s (mode 600) with %d secret(s) at %s\n", output, len(names), where)
	}
	return nil
}

func secretEnvFolder(templatePath, folder string) (string, error) {
	if strings.TrimSpace(folder) != "" {
		return secretPathFlag(folder)
	}
	abs, err := filepath.Abs(templatePath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	root, err := workspace.FindRoot(dir)
	if err != nil {
		return "", clibase.UserError("path_required", "cannot tell which folder "+templatePath+" belongs to: it is not in a gitslice workspace", "Pass --path /<account>/<folder>, or --slice.")
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", clibase.UserError("path_required", "cannot tell which folder "+templatePath+" belongs to", "Pass --path /<account>/<folder>, or --slice.")
	}
	return secretPathFlag("/" + filepath.ToSlash(rel))
}

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
