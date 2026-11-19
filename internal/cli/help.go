package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

type helpTopic struct {
	Name    string
	Summary string
	Body    string
}

var cliHelpTopics = []helpTopic{
	{
		Name:    "environment",
		Summary: "Environment variables used by gs",
		Body: `Environment

GS_SERVER_ADDR
  Default gRPC server address for commands that accept --server.
  When unset, gs defaults to the production endpoint api.gitslice.io:443
  (TLS). Use 127.0.0.1:50051 for a local server.

GITSLICE_GRPC_ADDR, GITSLICE_SERVER_ADDR
  Compatibility aliases for GS_SERVER_ADDR when GS_SERVER_ADDR is unset.

GS_WEB_URL
  Default browser UI base URL for gs auth signup and gs browse.

GITSLICE_WEB_URL
  Compatibility alias for GS_WEB_URL when GS_WEB_URL is unset.

GS_GATEWAY_URL
  Default HTTP JSON gateway URL used by the signup approval page.

GITSLICE_GATEWAY_URL
  Compatibility alias for GS_GATEWAY_URL when GS_GATEWAY_URL is unset.

GS_HTTP_ADDR, GITSLICE_HTTP_ADDR
  Compatibility sources for the default gateway URL when no gateway URL is set.

GS_CLIENT_CACHE_DIR
  Override the shared local content-addressed object cache root.

GITSLICE_CLIENT_CACHE_DIR
  Compatibility alias for GS_CLIENT_CACHE_DIR when GS_CLIENT_CACHE_DIR is unset.

GS_NO_UPDATE_CHECK=1
  Stop gs from looking up the latest release once a day and saying on
  stderr when a newer one exists. gs upgrade --check still looks.

NO_COLOR
  Disable ANSI color output.

TERM=dumb
  Disable color and sticky interactive shell headers.
`,
	},
	{
		Name:    "formatting",
		Summary: "Text, JSON, and error output conventions",
		Body: `Formatting

By default, gs prints human-readable text to stdout.

Use --json or --format json for stable machine-readable output:

  gs status --json
  gs context --format json

Use --json=field,field to select top-level fields:

  gs auth status --json=signed_in,server_addr
  gs context --json=server_addr,active_slice

Use --jq to filter structured command output with jq syntax:

  gs auth status --jq .reason
  gs context --jq '{slice: .active_slice, source: .active_slice_source}'

Use --template to format structured command output with Go text/template over
the same JSON-shaped fields:

  gs auth status --template '{{.signed_in}}'
  gs context --template '{{.active_slice}} {{.active_slice_source}}'

Diagnostics, progress, and errors are written to stderr. Long uploads, syncs
and hydrates report how far they have got there: one line that rewrites
itself on a terminal, otherwise a line every few seconds. --json and --quiet
turn progress off. When a command is run with --json, --format json, --jq,
or --template, error output has this shape:

  {
    "error": {
      "code": "stable_snake_case_code",
      "message": "human-readable message",
      "hint": "optional next action",
      "retriable": false
    }
  }

Use --quiet to suppress non-essential text output and --no-color or NO_COLOR to
disable ANSI color. Scripts should consume documented JSON fields from gs schema.
`,
	},
	{
		Name:    "exit-codes",
		Summary: "Exit codes returned by gs",
		Body: `Exit Codes

0
  Success.

1
  General command failure.

2
  Command canceled.

3
  gs sync finished but left files with conflict markers; resolve them, then
  run gs modify.

4
  Authentication is missing, invalid, or rejected by the server.

Commands may print additional structured error details on stderr when --json,
--format json, --jq, or --template is set.
`,
	},
	{
		Name:    "paths",
		Summary: "Account-rooted path rules",
		Body: `Paths

Server paths are canonical account-rooted paths:

  /nic/notes/readme.md
  /acme/payment/app.go

gs fs commands always require absolute server paths and are scoped to the
signed-in user's home slice. For user nic, mutations must stay under /nic.

Workspaces materialize canonical account-rooted paths below the workspace root.
A server file at /nic/hello/readme.md appears locally as:

  nic/hello/readme.md

Inside gs shell, relative paths are resolved from the shell's current server
directory. Absolute paths are still canonical server paths.
`,
	},
	{
		Name:    "slices",
		Summary: "Home slices, custom slices, and slice slugs",
		Body: `Slices

A slice is a named view over one or more account-rooted server path prefixes.
Slice references use the canonical form:

  <account>/<slice>

When the CLI is signed in, commands that accept slice references also accept a
bare slice slug and expand it to the signed-in account. For user nic, home
means nic/home.

The reserved home slice is created by signup:

  nic/home

The home slice covers the account root /nic, not /nic/home. Custom personal
slices use other slugs and must cover existing paths under the same account,
for example:

  nic/dotfiles -> /nic/dotfiles
  nic/tools    -> /nic/tools

Workspaces bind to exactly one slice. Changesets created from a workspace use
that bound slice as the authoring slice.
`,
	},
	{
		Name:    "automations",
		Summary: "Run HTTP hooks, JS workers, bash containers, or checks on folder events or manually",
		Body: `Automations

An automation lives in <folder>/.gitslice/automations.yaml and runs on events
affecting that folder and below (push, tag.created, changeset.*,
check_run.completed) or manually (on: [manual]) with typed inputs and
folder-scoped secrets. Its action is:
  - http:   signed HTTP POST of the event payload
  - worker: JavaScript ES module under .gitslice/ on Cloudflare Dynamic Workers
  - bash:   bash command or script in an ephemeral Cloudflare Container
  - check:  runs checks from the folder's checks.yaml on the CI runner pool
            (mark them started_by: automation so they wait for the automation)

  version: 1
  automations:
    deploy-staging:
      description: Deploy billing service to staging
      on: [manual]
      manual:
        roles: [admin, writer]               # or [admin]
        require_human: true                  # block agent accounts
        confirm: "Deploy billing to staging?"
      inputs:
        version: { type: string, required: true }
        region:  { type: choice, options: [us-central1, europe-west1], default: us-central1 }
      secrets: [CLOUDFLARE_API_TOKEN]
      network: [api.cloudflare.com]          # http-gate: sealed secrets unwrapped at egress
      action:
        bash:
          runtime: cloud                     # base | node | python | go | cloud
          size: basic                        # lite | basic | standard-1 | standard-2
          checkout: folder                   # none | folder | account
          run: |
            curl -fsS -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
              https://api.cloudflare.com/client/v4/user/tokens/verify
    notify:
      on: [push]
      refs: [main]
      secrets: [HOOK_TOKEN]
      action:
        http:
          url: https://hooks.example.com/x?t=${{ secrets.HOOK_TOKEN }}
          sign: HOOK_TOKEN                   # X-Gitslice-Signature-256

  gs auto ls [folder]                        # automations applying at a folder
  gs auto runs --changeset <id>              # runs of a changeset (or --automation, --account)
  gs auto show <run-id>
  gs auto logs <run-id> [--follow]
  gs auto run <id> [-i k=v]... [--yes] [-f]  # invoke a manual automation
  gs auto rerun <run-id>
  gs auto cancel <run-id>

Definitions and scripts always come from the head of the target ref (never an
unsubmitted draft), and any change under .gitslice/ requires account owner or
admin approval. For bash actions, secrets are sealed behind Gate in http-gate
mode (when network: is set); raw TCP tools (psql, ssh, aws, gcloud) opt into
egress: direct and reveal: [SECRET] and read secrets via $NAME or
$GITSLICE_SECRETS_DIR/<NAME> (/run/secrets). See https://gitslice.io/doc/automations.
`,
	},
	{
		Name:    "secrets",
		Summary: "Pass secret values to the CI checks of some folders",
		Body: `Secrets

A secret is a value Gitslice keeps encrypted and passes to CI checks as an
environment variable named after it. It belongs to an account and applies to
folders of the account and everything below them. A check gets the secrets
that apply at the folder of the checks.yaml that defines it, whichever slice's
change runs it. The account's owners and admins manage secrets with gs secret
or on the account's Secrets page in the web app. No page shows a value; only
owners and admins can read one back with gs secret env (see Local env files).

  printf %s "$TOKEN" | gs secret create DEPLOY_TOKEN --path /acme/api --path /acme/worker
  printf %s "$TOKEN" | gs secret create NPM_TOKEN --path /acme
  printf %s "$TOKEN" | gs secret create WEB_TOKEN --slice web
  gs secret ls [--account acme]
  gs secret update <secret-id> --add-path /acme/web
  printf %s "$NEW" | gs secret update <secret-id> --value
  gs secret rm <secret-id>

--path takes an absolute folder; /acme is the whole account. --slice is short
for the folders the slice includes. In a terminal, leave out the pipe and type
the value; it is not echoed.

Local env files
  The account's owners and admins can fill an env template from the secrets at
  its folder. In the template, write NAME=gitslice-secret:SECRET_NAME:

  gs secret env gitslice/gitslice/env.prod.template -o .env.prod

  In a workspace the template's folder is found from where it sits; elsewhere
  pass --path /account/folder. The file is written with mode 600, values are
  never printed to a terminal, and the server logs who read which names.
  Nobody else can read a value.

Folders and precedence
  A folder has at most one secret of each name. Where several secrets of one
  name apply, the one on the closest folder wins: DEPLOY_TOKEN on /acme/web
  overrides DEPLOY_TOKEN on /acme for checks under /acme/web.

  gs slice secret set/rm/ls work on one slice's folders. set changes a secret
  that only those folders have, or creates one there; rm takes the slice's
  folders out of a secret that has others.

Who can read a secret
  A secret on a folder reaches the CI daemon of every slice whose changes run
  a check defined in it, and every writer who can add a check there: writers
  of slices that include the folder, a folder above it, or one inside it.
  Choose folders with care. Values of 4 or more characters are shown as *** in
  check logs and summaries, but that does not stop a check that sets out to
  leak one.
`,
	},
}

func (r Runner) helpCommand(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "help [command|topic]",
		Short: "Help about any command or topic",
		Long:  "Help provides command help and Gitslice topic guides.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if err := root.Help(); err != nil {
					return err
				}
				r.writeHelpTopicList()
				return nil
			}
			if len(args) == 1 {
				if topic, ok := findHelpTopic(args[0]); ok {
					return writeHelpTopic(r.Stdout, topic)
				}
			}
			target, _, err := root.Find(args)
			if err == nil && target != nil && target != root && target != cmd {
				return target.Help()
			}
			return userError("unknown_help_topic", "unknown help topic or command: "+strings.Join(args, " "), "Run gs help to list available help topics.")
		},
	}
	for _, topic := range cliHelpTopics {
		topic := topic
		cmd.AddCommand(&cobra.Command{
			Use:   topic.Name,
			Short: topic.Summary,
			Args:  noArgs("gs help " + topic.Name),
			RunE: func(cmd *cobra.Command, args []string) error {
				return writeHelpTopic(r.Stdout, topic)
			},
		})
	}
	withSchemaCollapsed(cmd, "gs help <topic>", "show help for a command or CLI topic")
	withSchemaOutputFields(cmd)
	return cmd
}

func (r Runner) writeHelpTopicList() {
	fmt.Fprintln(r.Stdout)
	fmt.Fprintln(r.Stdout, "HELP TOPICS")
	for _, topic := range cliHelpTopics {
		fmt.Fprintf(r.Stdout, "  %-13s %s\n", topic.Name+":", topic.Summary)
	}
}

func findHelpTopic(name string) (helpTopic, bool) {
	for _, topic := range cliHelpTopics {
		if topic.Name == name {
			return topic, true
		}
	}
	return helpTopic{}, false
}

func writeHelpTopic(w io.Writer, topic helpTopic) error {
	_, err := fmt.Fprint(w, strings.TrimRight(topic.Body, "\n")+"\n")
	return err
}
