package cli

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed trigger_local_harness.mjs
var localAutomationHarness string

// localWholeURLSecretMessage matches the dispatcher's
// (trigger-dispatch/src/entry.ts WHOLE_URL_SECRET_MESSAGE).
const localWholeURLSecretMessage = "a secret cannot be the whole URL of a fetch: the gate opens secrets only where they cannot change the request's origin. " +
	"Write the scheme and host in the script and put the secret in the path, query or a header, " +
	"such as fetch(`https://api.example.com/hook?${gs.secret(\"HOOK_QUERY\")}`)."

// localAutomationsFile is the part of .gitslice/automations.yaml a local
// run needs. The server's parser (internal/triggers) stays out of gs.
type localAutomationsFile struct {
	Automations map[string]localAutomation `yaml:"automations"`
}

type localAutomation struct {
	On      []string `yaml:"on"`
	Secrets []string `yaml:"secrets"`
	Reveal  []string `yaml:"reveal"`
	Network []string `yaml:"network"`
	Action  struct {
		Worker string `yaml:"worker"`
	} `yaml:"action"`
}

type localAutomationConfig struct {
	Script          string            `json:"script"`
	Root            string            `json:"root"`
	FolderDir       string            `json:"folderDir"`
	Event           map[string]any    `json:"event"`
	Secrets         []string          `json:"secrets"`
	Reveal          []string          `json:"reveal"`
	Network         []string          `json:"network"`
	SecretValues    map[string]string `json:"secretValues"`
	Callbacks       []string          `json:"callbacks"`
	WholeURLMessage string            `json:"wholeUrlMessage"`
}

// findLocalAutomation finds the .gitslice/automations.yaml above dir that
// defines automationID in account: the folder <root>/<account>/<id's dir>.
// It returns the folder's directory, the workspace root (the directory the
// account's folders are in) and the automation.
func findLocalAutomation(dir, account, automationID string) (string, string, string, localAutomation, error) {
	id := strings.Trim(strings.TrimSpace(automationID), "/")
	idDir, name := "", id
	if i := strings.LastIndex(id, "/"); i >= 0 {
		idDir, name = id[:i], id[i+1:]
	}
	suffix := string(filepath.Separator) + account
	if idDir != "" {
		suffix += string(filepath.Separator) + filepath.FromSlash(idDir)
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if strings.HasSuffix(d, suffix) {
			data, err := os.ReadFile(filepath.Join(d, ".gitslice", "automations.yaml"))
			if err == nil {
				var file localAutomationsFile
				if err := yaml.Unmarshal(data, &file); err != nil {
					return "", "", "", localAutomation{}, userError("invalid_automations", fmt.Sprintf("%s: %v", filepath.Join(d, ".gitslice", "automations.yaml"), err), "")
				}
				if a, ok := file.Automations[name]; ok {
					return d, strings.TrimSuffix(d, suffix), name, a, nil
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", "", "", localAutomation{}, err
			}
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return "", "", "", localAutomation{}, userError("automation_not_found",
		fmt.Sprintf("no .gitslice/automations.yaml defining %s at /%s/%s in or above %s", name, account, idDir, dir),
		"Run gs automation run --local from inside the workspace that has the automation's folder.")
}

// runLocalAutomation runs a worker automation's script from the workspace's
// files with node and a stand-in gs, so a script can be tried before it
// lands: no server, no real secrets unless given, callbacks from files.
func (r Runner) runLocalAutomation(ctx context.Context, opts commandOptions, account, automationID string, rawInputs, rawSecrets, callbackFiles []string) error {
	if strings.TrimSpace(account) == "" {
		ws, err := r.readWorkspaceConfig()
		if err != nil || ws.Account == "" {
			return userError("account_required", "pass --account, or run inside a workspace", "")
		}
		account = ws.Account
	}
	inputs := map[string]string{}
	for _, raw := range rawInputs {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return userError("invalid_argument", "--input "+raw+" is not key=value", "")
		}
		inputs[strings.TrimSpace(key)] = value
	}
	secretValues := map[string]string{}
	for _, raw := range rawSecrets {
		name, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return userError("invalid_argument", "--secret takes NAME=value", "")
		}
		secretValues[strings.TrimSpace(name)] = value
	}
	folderDir, root, name, auto, err := findLocalAutomation(r.cwd(), account, automationID)
	if err != nil {
		return err
	}
	if auto.Action.Worker == "" {
		return userError("not_a_worker", "automation "+automationID+" has no worker action; --local runs worker scripts only", "")
	}
	gsDir := filepath.Join(folderDir, ".gitslice")
	script := filepath.Clean(filepath.Join(gsDir, filepath.FromSlash(auto.Action.Worker)))
	if rel, err := filepath.Rel(gsDir, script); err != nil || strings.HasPrefix(rel, "..") {
		return userError("invalid_automations", "worker "+auto.Action.Worker+" must stay inside the .gitslice folder", "")
	}
	for name := range secretValues {
		if !containsString(auto.Secrets, name) {
			return userError("invalid_argument", "--secret "+name+": the automation does not list it in secrets", "")
		}
	}
	var callbacks []string
	for _, path := range callbackFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		callbacks = append(callbacks, string(data))
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return userError("node_required", "--local runs the script with node, which is not on PATH", "Install Node.js 20 or newer.")
	}
	id := strings.Trim(strings.TrimSpace(automationID), "/")
	folder := "/" + account
	if i := strings.LastIndex(id, "/"); i >= 0 {
		folder += "/" + id[:i]
	}
	meta := map[string]string{"id": id, "name": name, "folder": folder}
	event := map[string]any{
		"id": "evt_local", "event": "manual", "automation": meta, "trigger": meta,
		"sender":     map[string]string{"username": "local", "kind": "human"},
		"created_at": time.Now().UTC().Format(time.RFC3339), "definition_commit": "local",
	}
	if len(inputs) > 0 {
		event["inputs"] = inputs
	}
	cfgJSON, err := json.Marshal(localAutomationConfig{
		Script: script, Root: root, FolderDir: folderDir, Event: event,
		Secrets: auto.Secrets, Reveal: auto.Reveal, Network: auto.Network,
		SecretValues: secretValues, Callbacks: callbacks, WholeURLMessage: localWholeURLSecretMessage,
	})
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "gs-local-automation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	harness := filepath.Join(tmp, "harness.mjs")
	if err := os.WriteFile(harness, []byte(localAutomationHarness), 0o600); err != nil {
		return err
	}
	if !opts.Quiet {
		fmt.Fprintf(r.stderr(), "running %s locally (%s) with node\n", id, script)
	}
	cmd := exec.CommandContext(ctx, node, harness)
	cmd.Stdin = bytes.NewReader(cfgJSON)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.stderr()
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == 3:
		if !opts.Quiet {
			fmt.Fprintln(r.stderr(), "the run is waiting for a callback; pass --callback <file> to send one")
		}
		return nil
	case errors.As(err, &exit):
		return userError("automation_failed", "local run of "+id+" did not succeed", "")
	default:
		return err
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
