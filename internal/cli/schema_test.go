package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSchemaCommandEmitsMachineReadableContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"schema"}); err != nil {
		t.Fatalf("schema failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got struct {
		SchemaVersion string `json:"schema_version"`
		GlobalFlags   []struct {
			Name string `json:"name"`
		} `json:"global_flags"`
		Commands []struct {
			Use     string   `json:"use"`
			Aliases []string `json:"aliases"`
		} `json:"commands"`
		HelpTopics []struct {
			Name string `json:"name"`
		} `json:"help_topics"`
		ErrorOutput map[string]any `json:"error_output"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("schema output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != "v1" {
		t.Fatalf("unexpected schema version %q", got.SchemaVersion)
	}
	if len(got.Commands) == 0 {
		t.Fatal("schema did not include commands")
	}
	globalFlags := map[string]bool{}
	for _, flag := range got.GlobalFlags {
		globalFlags[flag.Name] = true
	}
	for _, want := range []string{"--format", "--json", "--jq", "--template"} {
		if !globalFlags[want] {
			t.Fatalf("schema missing global flag %q", want)
		}
	}
	uses := map[string]bool{}
	aliases := map[string][]string{}
	for _, command := range got.Commands {
		uses[command.Use] = true
		aliases[command.Use] = command.Aliases
	}
	for _, want := range []string{"gs auth token", "gs auth logout", "gs alias list", "gs alias set <name> <command>", "gs browse [account:slice | /account/path | workspace/path | web-page]", "gs init <slice|account:slice>", "gs import <source>", "gs sync", "gs workspace sync", "gs ci", "gs create", "gs modify", "gs submit [changeset]", "gs deps [dependency-tree]", "gs update-dependents [changeset]", "gs switch <changeset>", "gs up [changeset]", "gs down [steps]", "gs top", "gs bottom", "gs move <changeset> --onto <base|root>", "gs insert --base <changeset> --message <title>", "gs detach <changeset>", "gs log [-- <path>]", "gs show <commit-id-or-prefix>", "gs version", "gs completion <shell>", "gs fs ls [remote-path]", "gs fs cat <absolute-path>", "gs fs mkdir <absolute-path>", "gs help <topic>"} {
		if !uses[want] {
			t.Fatalf("schema missing %q", want)
		}
	}
	for _, removed := range []string{"gs repo import github <owner/repo-or-url>", "gs repository import github <owner/repo-or-url>", "gs cs create", "gs cs update", "gs cs submit [changeset]", "gs cs status [changeset]", "gs cs diff [changeset]", "gs cs list"} {
		if uses[removed] {
			t.Fatalf("schema still includes removed command %q", removed)
		}
	}
	for use, wantAlias := range map[string]string{
		"gs context":              "gs ctx",
		"gs config list":          "gs cfg list",
		"gs status":               "gs st",
		"gs slice list [account]": "gs slices list [account]",
	} {
		if !stringSliceContains(aliases[use], wantAlias) {
			t.Fatalf("schema aliases for %q missing %q: %#v", use, wantAlias, aliases[use])
		}
	}
	topics := map[string]bool{}
	for _, topic := range got.HelpTopics {
		topics[topic.Name] = true
	}
	for _, want := range []string{"environment", "formatting", "exit-codes", "paths", "slices"} {
		if !topics[want] {
			t.Fatalf("schema missing help topic %q", want)
		}
	}
	if got.ErrorOutput["stream"] != "stderr" {
		t.Fatalf("expected stderr error stream, got %#v", got.ErrorOutput["stream"])
	}
}

func TestSchemaCommandSupportsStructuredOutputFilters(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"schema", "--jq", `.global_flags[] | select(.name == "--jq") | .description`}); err != nil {
		t.Fatalf("schema --jq failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "filter structured output with a jq expression"; got != want {
		t.Fatalf("schema jq output = %q, want %q", got, want)
	}
}
