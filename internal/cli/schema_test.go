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

func TestSchemaOutputAnnotatedCommandsBindMachineOutputAndJSONRendering(t *testing.T) {
	doc := runSchemaForTest(t)
	machineOutputByUse := make(map[string][]string, len(doc.Commands))
	for _, cmd := range doc.Commands {
		machineOutputByUse[cmd.Use] = cmd.MachineOutput
	}

	r := Runner{}
	root := r.rootCommand()
	annotatedCanonicals := map[string]reflect.Type{}
	var walk func(ancestors []*cobra.Command, cmd *cobra.Command)
	walk = func(ancestors []*cobra.Command, cmd *cobra.Command) {
		if cmd == nil || cmd.Hidden || isSchemaExcludedCommand(cmd) {
			return
		}
		chain := append(append([]*cobra.Command(nil), ancestors...), cmd)
		canonical := schemaCommandCanonicalPath(root, chain)
		use := schemaCommandUse(root, chain)
		if cmd.Annotations[schemaOutputSetAnnotation] == "true" {
			if cmd.Annotations[schemaOutputAnnotation] == "" && cmd.Annotations[schemaOutputTypeAnnotation] == "" {
				if got := machineOutputByUse[use]; len(got) != 0 {
					t.Fatalf("command %q (%s) declares empty machine_output via withSchemaOutputFields, got %v", use, canonical, got)
				}
			} else {
				outType, ok := schemaOutputTypeForCommand(cmd)
				if !ok {
					t.Fatalf("command %q (%s) sets non-empty schema output annotation without registered Go type", use, canonical)
				}
				if outType.Kind() != reflect.Struct {
					t.Fatalf("command %q (%s) schema output type %v is not a struct", use, canonical, outType)
				}
				wantFields := structJSONFields(outType)
				if len(wantFields) == 0 {
					t.Fatalf("command %q (%s) schema output struct %v has no exported JSON fields", use, canonical, outType)
				}
				gotFields := machineOutputByUse[use]
				if !slices.Equal(gotFields, wantFields) {
					t.Fatalf("command %q (%s) machine_output = %v, want %v from %v", use, canonical, gotFields, wantFields, outType)
				}
				if _, dup := defaultSchemaCommandOutputs[canonical]; dup {
					t.Fatalf("command %q (%s) uses withSchemaOutput[%s] and should not duplicate defaultSchemaCommandOutputs", use, canonical, outType.Name())
				}

				// Verify writeSchemaOutput marshals the registered Go type using keys declared in machine_output.
				var buf bytes.Buffer
				jsonRunner := Runner{Stdout: &buf, Stderr: &bytes.Buffer{}}
				zeroVal := reflect.New(outType).Elem().Interface()
				if err := writeSchemaOutput(jsonRunner, commandOptions{Format: "json"}, zeroVal, func() error {
					t.Fatalf("textFn should not run in --json mode for %s", canonical)
					return nil
				}); err != nil {
					t.Fatalf("writeSchemaOutput(%s) failed: %v", canonical, err)
				}
				var decoded map[string]any
				if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
					t.Fatalf("writeSchemaOutput(%s) produced invalid JSON: %v\n%s", canonical, err, buf.String())
				}
				for key := range decoded {
					if !slices.Contains(wantFields, key) {
						t.Fatalf("writeSchemaOutput(%s) emitted undeclared JSON key %q (declared: %v)", canonical, key, wantFields)
					}
				}
				annotatedCanonicals[canonical] = outType
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

	for _, wantCanonical := range []string{
		"gs auth status",
		"gs blame",
		"gs check allow",
		"gs feedback list",
		"gs group add",
		"gs group create",
		"gs group rm",
		"gs group show",
		"gs intent",
		"gs slice create",
		"gs slice info",
		"gs slice members",
		"gs slice set-ci-daemon",
		"gs slice share",
		"gs slice update",
		"gs tag create",
		"gs version",
	} {
		if _, ok := annotatedCanonicals[wantCanonical]; !ok {
			t.Fatalf("expected %q to be annotated with withSchemaOutput[T], got %v", wantCanonical, annotatedCanonicals)
		}
	}

	// Verify renderCommandOutput / writeSchemaOutput text and --quiet modes too.
	var textBuf bytes.Buffer
	textRunner := Runner{Stdout: &textBuf, Stderr: &bytes.Buffer{}}
	called := false
	if err := renderCommandOutput(textRunner, commandOptions{Format: "text"}, versionOutput{Version: "1.2.3", GoVersion: "go1.26"}, func() error {
		called = true
		textBuf.WriteString("human text\n")
		return nil
	}); err != nil {
		t.Fatalf("renderCommandOutput text mode failed: %v", err)
	}
	if !called || textBuf.String() != "human text\n" {
		t.Fatalf("unexpected text mode result: called=%v out=%q", called, textBuf.String())
	}

	textBuf.Reset()
	if err := writeSchemaOutput(textRunner, commandOptions{Format: "text", Quiet: true}, versionOutput{Version: "1.2.3"}, func() error {
		t.Fatal("textFn should not be called when Quiet is true")
		return nil
	}); err != nil {
		t.Fatalf("writeSchemaOutput quiet mode failed: %v", err)
	}
	if textBuf.Len() != 0 {
		t.Fatalf("expected empty output in quiet mode, got %q", textBuf.String())
	}
}
