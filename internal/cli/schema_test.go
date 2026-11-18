package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type parsedSchemaDoc struct {
	SchemaVersion string `json:"schema_version"`
	GlobalFlags   []struct {
		Name        string   `json:"name"`
		Shorthand   string   `json:"shorthand"`
		Description string   `json:"description"`
		Values      []string `json:"values"`
		Default     string   `json:"default"`
	} `json:"global_flags"`
	Commands []struct {
		Use           string   `json:"use"`
		Summary       string   `json:"summary"`
		Aliases       []string `json:"aliases"`
		Args          []string `json:"args"`
		Flags         []string `json:"flags"`
		WritesStdout  bool     `json:"writes_stdout"`
		MachineOutput []string `json:"machine_output"`
	} `json:"commands"`
	HelpTopics []struct {
		Name string `json:"name"`
	} `json:"help_topics"`
	ErrorOutput map[string]any `json:"error_output"`
}

func runSchemaForTest(t *testing.T) parsedSchemaDoc {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"schema"}); err != nil {
		t.Fatalf("schema failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got parsedSchemaDoc
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("schema output is not JSON: %v\n%s", err, stdout.String())
	}
	return got
}

func TestSchemaCommandEmitsMachineReadableContract(t *testing.T) {
	got := runSchemaForTest(t)
	if got.SchemaVersion != "v1" {
		t.Fatalf("unexpected schema version %q", got.SchemaVersion)
	}
	if len(got.Commands) == 0 {
		t.Fatal("schema did not include commands")
	}
	globalFlags := map[string]string{}
	for _, flag := range got.GlobalFlags {
		globalFlags[flag.Name] = flag.Shorthand
	}
	for _, want := range []string{"--format", "--json", "--jq", "--template", "--directory"} {
		if _, ok := globalFlags[want]; !ok {
			t.Fatalf("schema missing global flag %q", want)
		}
	}
	if globalFlags["--directory"] != "-C" {
		t.Fatalf("expected --directory shorthand -C, got %q", globalFlags["--directory"])
	}

	uses := map[string]bool{}
	aliases := map[string][]string{}
	flagsByUse := map[string][]string{}
	for _, command := range got.Commands {
		if uses[command.Use] {
			t.Fatalf("duplicate command entry in schema: %q", command.Use)
		}
		uses[command.Use] = true
		aliases[command.Use] = command.Aliases
		flagsByUse[command.Use] = command.Flags
	}
	for _, want := range []string{
		"gs auth token",
		"gs auth logout",
		"gs alias list",
		"gs alias set <name> <command>",
		"gs browse [account:slice | /account/path | workspace/path | web-page]",
		"gs init <slice|account:slice> [dir]",
		"gs import <source>",
		"gs sync",
		"gs workspace sync",
		"gs ci",
		"gs create",
		"gs modify",
		"gs submit [changeset]",
		"gs deps [dependency-tree]",
		"gs update-dependents [changeset]",
		"gs switch <changeset>",
		"gs up [changeset]",
		"gs down [steps]",
		"gs top",
		"gs bottom",
		"gs move <changeset> --onto <base|root>",
		"gs insert --base <changeset> --message <title>",
		"gs detach <changeset>",
		"gs log [-- <path>]",
		"gs show <commit-id-or-prefix|changeset>",
		"gs version",
		"gs completion <shell>",
		"gs fs ls [remote-path]",
		"gs fs cat <absolute-path>",
		"gs fs mkdir <absolute-path>",
		"gs help <topic>",
		// Previously missing command families now derived automatically:
		"gs account create-org <slug>",
		"gs claims list",
		"gs tag create <name>",
		"gs secret ls",
		"gs trigger ls [folder]",
		"gs slice mirror <slice|account:slice>",
		"gs slice secret set <slice|account:slice> <NAME> [value]",
		"gs slice set-ci-daemon <slice|account:slice> <daemon-id>",
		"gs agent start",
		"gs agent status",
		"gs agent stop",
		"gs agent doctor",
		"gs agent install",
		"gs cs explain [changeset]",
		"gs cs versions [changeset]",
		"gs cs approve <changeset>",
		"gs cs abandon [changeset]",
	} {
		if !uses[want] {
			t.Fatalf("schema missing %q", want)
		}
	}
	for _, removed := range []string{
		"gs repo import github <owner/repo-or-url>",
		"gs repository import github <owner/repo-or-url>",
		"gs cs create",
		"gs cs update",
		"gs cs submit [changeset]",
		"gs cs diff [changeset]",
	} {
		if uses[removed] {
			t.Fatalf("schema still includes removed command %q", removed)
		}
	}
	for use, wantFlags := range map[string][]string{
		"gs init <slice|account:slice> [dir]": {"--lazy", "--read-only", "--prefetch"},
		"gs status":                           {"--local", "--relative"},
		"gs cs list":                          {"--slice", "--status", "--limit"},
		"gs fs write <absolute-path> (--text text|--stdin)": {"--no-submit", "--text", "--stdin"},
	} {
		for _, wf := range wantFlags {
			if !slices.Contains(flagsByUse[use], wf) {
				t.Fatalf("schema flags for %q = %v, missing %q", use, flagsByUse[use], wf)
			}
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

func TestSchemaParityWithLiveCobraCommandTree(t *testing.T) {
	got := runSchemaForTest(t)
	byUse := make(map[string]struct {
		Summary string
		Flags   []string
	}, len(got.Commands))
	for _, cmd := range got.Commands {
		byUse[cmd.Use] = struct {
			Summary string
			Flags   []string
		}{Summary: cmd.Summary, Flags: cmd.Flags}
	}

	allowedLegacyExcluded := map[string]bool{
		"gs cs create": true,
		"gs cs update": true,
		"gs cs submit": true,
		"gs cs diff":   true,
	}

	r := Runner{}
	root := r.rootCommand()
	var walk func(ancestors []*cobra.Command, cmd *cobra.Command)
	walk = func(ancestors []*cobra.Command, cmd *cobra.Command) {
		if cmd == nil {
			return
		}
		chain := append(append([]*cobra.Command(nil), ancestors...), cmd)
		canonical := schemaCommandCanonicalPath(root, chain)
		use := schemaCommandUse(root, chain)

		if cmd.Hidden {
			if _, ok := byUse[use]; ok {
				t.Fatalf("hidden command %q leaked into gs schema", use)
			}
			return
		}
		if isSchemaExcludedCommand(cmd) {
			if !allowedLegacyExcluded[canonical] {
				t.Fatalf("unexpected schema-excluded command %q (not in legacy compatibility allowlist)", canonical)
			}
			if _, ok := byUse[use]; ok {
				t.Fatalf("excluded command %q appeared in gs schema", use)
			}
			return
		}
		if isSchemaGroupCommand(cmd) {
			if _, ok := byUse[use]; ok {
				t.Fatalf("group container command %q should not appear as runnable in gs schema", use)
			}
		} else {
			entry, ok := byUse[use]
			if !ok {
				t.Fatalf("non-hidden runnable Cobra command %q (%s) missing from gs schema", use, canonical)
			}
			if strings.TrimSpace(entry.Summary) == "" {
				t.Fatalf("command %q in gs schema has empty summary", use)
			}
			if !isSchemaCollapsedCommand(cmd) {
				wantFlags := collectSchemaCommandFlags(chain)
				if !slices.Equal(entry.Flags, wantFlags) {
					t.Fatalf("command %q flags = %v, want %v", use, entry.Flags, wantFlags)
				}
			}
		}
		if isSchemaCollapsedCommand(cmd) {
			return
		}
		for _, child := range cmd.Commands() {
			walk(chain, child)
		}
	}
	for _, child := range root.Commands() {
		walk(nil, child)
	}
}

func TestStructJSONFieldsHandlesEmbeddedStructs(t *testing.T) {
	got := structJSONFields(reflect.TypeFor[sharedSliceOutput]())
	for _, want := range []string{"id", "ref", "account", "slice", "required_human_approvals", "role"} {
		if !slices.Contains(got, want) {
			t.Fatalf("structJSONFields(sharedSliceOutput) = %v, missing %q", got, want)
		}
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
