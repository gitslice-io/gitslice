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
		Name:    "webhooks",
		Summary: "Send a slice's events to an HTTPS endpoint",
		Body: `Webhooks

A webhook POSTs a JSON event to an HTTPS URL when something happens in a
slice. The slice's owners and admins manage them with gs webhook or in the
web app's Slice Settings.

  gs webhook create --slice acme/payment --url https://example.com/hook \
    --event tag.created --event push --secret-stdin < secret.txt
  gs webhook list --slice acme/payment
  gs webhook ping <webhook-id>
  gs webhook deliveries <webhook-id>
  gs webhook redeliver <delivery-id>
  gs webhook update <webhook-id> --active false
  gs webhook delete <webhook-id> --yes

Events
  push                 a commit landed that changes paths the slice includes
  tag.created          a tag (release) was created
  changeset.created    a changeset was opened
  changeset.updated    a new patchset was uploaded
  changeset.approved   a changeset was approved
  changeset.submitted  a changeset landed
  changeset.abandoned  a changeset was abandoned
  check_run.completed  a check finished
  *                    all of the above
  ping                 always sent, on request (gs webhook ping)

Each body has id, event, created_at, slice, sender, plus a section for the
event: tag, changeset, commit or check_run.

Headers
  X-Gitslice-Event           the event name
  X-Gitslice-Event-ID        the event id; the same on retries and redeliveries
  X-Gitslice-Delivery        this delivery's id
  X-Gitslice-Hook-ID         the webhook's id
  X-Gitslice-Signature-256   sha256=<hex HMAC-SHA256 of the raw body, keyed by
                             the secret>, when the webhook has a secret

Verify the signature by computing the HMAC of the raw body yourself and
comparing in constant time.

Delivery
  Any 2xx answer within 10 seconds is success; redirects are not followed.
  Failures are retried after 1m, 5m, 30m, 2h, 6h and 12h, then given up.
  Deliveries can repeat and arrive out of order: drop duplicate event ids.
  URLs must be https and reach a public address.
`,
	},
	{
		Name:    "secrets",
		Summary: "Pass secret values to the CI checks of some slices",
		Body: `Secrets

A secret is a value Gitslice keeps encrypted and passes to CI checks as an
environment variable named after it. It belongs to an account and applies to
some of the account's slices, or to all of them. The account's owners and
admins manage secrets with gs secret or on the account's Secrets page in the
web app. No command or page shows a value again once it is set.

  printf %s "$TOKEN" | gs secret create DEPLOY_TOKEN --slice api --slice worker
  printf %s "$TOKEN" | gs secret create NPM_TOKEN --all-slices
  gs secret ls [--account acme]
  gs secret update <secret-id> --add-slice web
  printf %s "$NEW" | gs secret update <secret-id> --value
  gs secret rm <secret-id>

In a terminal, leave out the pipe and type the value; it is not echoed.

Scope
  A slice gets at most one secret of each name. One name can hold different
  values for different slices, and a secret that lists a slice overrides an
  all-slices secret of the same name there.

  gs slice secret set/rm/ls work on one slice. set changes a secret that only
  that slice has, or creates one; rm takes the slice out of a shared secret's
  scope.

Who can read a secret
  Every writer of a slice in its scope can read it from a check, and so can
  the slice's CI daemon. Widen a scope with care.
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
