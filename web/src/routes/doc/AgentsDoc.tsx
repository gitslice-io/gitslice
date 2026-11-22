import { Link } from "@tanstack/react-router";

import {
  BulletPoint,
  CommandBlock,
  DocLink,
  DocSectionCard,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";

const linkClass =
  "font-medium text-zinc-950 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-950 dark:text-zinc-50 dark:decoration-zinc-600 dark:hover:decoration-zinc-50";

export function AgentsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Agents"
        description="Coding agents work in Gitslice through the gs CLI or MCP, with their own identity, scoped slice roles, and optional human-in-the-loop submit gates."
      />

      <DocSectionCard id="agent-skill" title="Install the official Gitslice Agent Skill">
        <p>
          Coding agents that support Agent Skills (Claude Code, Codex, Cursor, Gemini, Antigravity)
          can load Gitslice workflows automatically from{" "}
          <a className={linkClass} href="/SKILL.md">
            gitslice.io/SKILL.md
          </a>{" "}
          (or{" "}
          <a className={linkClass} href="/llms.txt">
            gitslice.io/llms.txt
          </a>
          ). Install the CLI and skill together in one line:
        </p>
        <CommandBlock label="install gs cli and agent skill">{`curl -fsSL https://gitslice.io/install.sh | GS_INSTALL_SKILL=1 sh

# Or install the skill into detected agent directories (~/.agents, ~/.claude, ~/.gemini, ~/.cursor, ~/.codex)
gs skill install
gs skill install --project   # install into ./.agents/skills/gitslice/SKILL.md`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard id="agent-invites" title="Option 1 (Fastest): Connect an agent with a single-use invite">
        <p>
          From any slice&apos;s <span className="font-semibold text-zinc-950 dark:text-zinc-50">Settings</span>{" "}
          page (<span className="font-semibold text-zinc-950 dark:text-zinc-50">Connect an agent</span>)
          or from your terminal, mint a short-lived single-use <InlineCode>gsi_...</InlineCode>{" "}
          join token bound to a slice and role. Give the token (or copyable prompt) to your agent
          and it registers, links ownership to you, and receives slice access in one command — no
          email claim round-trip needed:
        </p>
        <CommandBlock label="single-use agent invite commands">{`# On the human's machine (or in Slice Settings → Connect an agent):
gs auth create-agent-invite --slice acme/payments --role contributor

# On the agent's machine (one idempotent command):
gs onboard --join gsi_... --install-skill --json
# Or directly:
gs auth join gsi_...`}</CommandBlock>
        <p>
          Manage unused tokens with <InlineCode>gs auth list-agent-invites</InlineCode> and{" "}
          <InlineCode>gs auth revoke-agent-invite &lt;invite-id&gt;</InlineCode>.
        </p>
      </DocSectionCard>

      <DocSectionCard id="agent-signup" title="Option 2: Let an agent self-register with your email">
        <p>
          An agent can also create its own account without a browser or an invite token. Use{" "}
          <InlineCode>gs onboard</InlineCode> (idempotent, auto-generates a unique username if
          omitted) or <InlineCode>gs auth register-agent</InlineCode>, and optionally request access
          to a slice upfront:
        </p>
        <CommandBlock label="agent self-registration commands">{`curl -fsSL https://gitslice.io/install.sh | GS_INSTALL_SKILL=1 sh
gs onboard --email you@example.com --slice acme/payments --role writer --install-skill --json

# Or lower-level registration with an explicit username:
gs auth register-agent --username release-bot --email you@example.com --request-slice acme/payments --request-role writer`}</CommandBlock>
        <p>
          Usernames are 4–63 lowercase letters, digits, and <InlineCode>-</InlineCode> (not
          starting or ending with <InlineCode>-</InlineCode>) and must be unique. If registration
          times out, re-run the exact same command — it resumes safely.
        </p>
        <p>
          To claim the agent, open{" "}
          <Link className={linkClass} to="/claims">
            gitslice.io/claims
          </Link>{" "}
          while signed in with an account whose verified email matches the address the agent
          registered with, then click{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">Accept &amp; grant</span>{" "}
          (or <span className="font-semibold text-zinc-950 dark:text-zinc-50">Accept</span>). You
          become a co-owner and grant the requested slice in one click; the agent keeps its access.
          From a terminal:
        </p>
        <CommandBlock label="claim agent commands">{`gs claims list
gs claims accept <agent-subject-id>              # grants any requested slice by default
gs claims accept <agent-subject-id> --grant-slice=false`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard title="Option 3: Provision a scoped agent as an owner">
        <p>
          If you prefer to create an agent upfront and hand it a ready-made API key, use{" "}
          <InlineCode>gs agent create</InlineCode>. It prints the initial API key once to stdout
          (or as <InlineCode>secret</InlineCode> under <InlineCode>--json</InlineCode>), and the
          agent signs in with <InlineCode>gs auth use-key</InlineCode>:
        </p>
        <CommandBlock label="delegated agent creation commands">{`# On the owner's machine: create the agent and grant contributor access to one slice
gs agent create triage-bot --slice acme/payments --role contributor

# On the agent's machine: install the key from stdin (or --key-file)
printf %s "$AGENT_KEY" | gs auth use-key

# Rotate or revoke a lost key later
gs auth keys create --agent triage-bot
gs auth keys list --agent triage-bot
gs auth keys revoke <key-id>`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard id="agent-web-login" title="Sign in to the web UI as an agent">
        <p>
          Agents (or owners inspecting an agent&apos;s view) can also sign in to the Gitslice web
          app with a short-lived, revocable session. From the terminal, run{" "}
          <InlineCode>gs auth web-login</InlineCode> to mint an expiring{" "}
          <InlineCode>purpose=&quot;web&quot;</InlineCode> child key and get a browser sign-in URL
          (the credential travels in the <InlineCode>#key=...</InlineCode> fragment, never in HTTP
          request logs):
        </p>
        <CommandBlock label="agent web sign-in commands">{`# Mint a 24h browser sign-in link for the signed-in agent
gs auth web-login

# Or scope the browser session to one slice and role, and open it right away
gs auth web-login --expires 2h --slice acme/payments --role reader --open`}</CommandBlock>
        <p>
          You can also paste a <InlineCode>gsk_...</InlineCode> API key into the{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">Agent API key</span> tab
          on{" "}
          <Link className={linkClass} reloadDocument to="/login">
            /login
          </Link>{" "}
          (or{" "}
          <Link className={linkClass} to="/agent-login">
            /agent-login
          </Link>
          ). Long-lived keys are automatically exchanged for a 24-hour child key stored in an{" "}
          <InlineCode>HttpOnly</InlineCode> cookie. Signing out in the top bar revokes the web
          session key immediately without affecting the agent&apos;s CLI credential, and
          human-only submit gates remain enforced.
        </p>
      </DocSectionCard>

      <DocSectionCard title="Keep agents safe and coordinated">
        <ul className="space-y-2">
          <BulletPoint label="Least-privilege slice roles:">
            Grant untrusted or autonomous agents the <InlineCode>contributor</InlineCode> role
            instead of <InlineCode>writer</InlineCode>. Contributors can open changesets and push
            patchsets, but their CI runs without secrets until a writer or admin reviews the
            patchset and runs <InlineCode>gs check allow &lt;changeset&gt;</InlineCode>.
          </BulletPoint>
          <BulletPoint label="Human approval gates:">
            Require at least one human review before submit with{" "}
            <InlineCode>gs slice update acme/payments --required-human-approvals 1</InlineCode>{" "}
            (or scope eligible reviewers to an account group with{" "}
            <InlineCode>--approvers @acme/reviewers</InlineCode>).
          </BulletPoint>
          <BulletPoint label="Parallel coordination:">
            Agents working in the same slice can reserve paths with{" "}
            <InlineCode>gs intent &apos;src/billing/**&apos; --for 2h -m &quot;fix retries&quot;</InlineCode>{" "}
            to warn teammates and other agents of overlap before edits collide.
          </BulletPoint>
          <BulletPoint label="Cheap experimentation:">
            In a <DocLink section="lazy">lazy workspace</DocLink>, agents can snapshot local edits
            with <InlineCode>gs checkpoint</InlineCode>, undo a dead end with{" "}
            <InlineCode>gs rewind</InlineCode>, or branch an alternative approach with{" "}
            <InlineCode>gs fork</InlineCode> without uploading anything.
          </BulletPoint>
          <BulletPoint label="Machine-readable CLI & remediation:">
            Most commands accept <InlineCode>--json</InlineCode>,{" "}
            <InlineCode>gs schema [command...]</InlineCode> inspects flags and output fields on
            demand, and structured JSON errors include <InlineCode>remediation_command</InlineCode>{" "}
            and <InlineCode>remediation_url</InlineCode>. In scheduled jobs,{" "}
            <InlineCode>gs create --all --if-changed --message &quot;...&quot; --submit</InlineCode>{" "}
            exits 0 cleanly when nothing changed and otherwise waits for required checks before
            landing.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard id="mcp" title="MCP server: hosted HTTP or local stdio">
        <p>
          Connect any MCP client with zero local installation using the hosted Streamable HTTP
          endpoint at <InlineCode>https://gitslice.io/mcp</InlineCode>, authenticated with an
          agent API key (<InlineCode>Authorization: Bearer gsk_...</InlineCode>):
        </p>
        <CommandBlock label="hosted Streamable HTTP MCP config">{`{
  "mcpServers": {
    "gitslice": {
      "url": "https://gitslice.io/mcp?slice=acme/payments",
      "headers": {
        "Authorization": "Bearer gsk_..."
      }
    }
  }
}`}</CommandBlock>
        <p>
          Or run the local stdio server via the CLI (<InlineCode>gs mcp</InlineCode>):
        </p>
        <CommandBlock label="MCP stdio server command">{`# Configure in your MCP client as the stdio server command:
gs mcp --slice acme/payments

# Optional: log every server and tool call as JSON lines
GS_TRACE=/tmp/gs-mcp-trace.jsonl gs mcp --slice acme/payments`}</CommandBlock>
        <p>
          Exposed tools: <InlineCode>read_file</InlineCode>, <InlineCode>list_directory</InlineCode>
          , <InlineCode>search</InlineCode> (line regex, like <InlineCode>grep -n</InlineCode>),{" "}
          <InlineCode>history</InlineCode>, <InlineCode>blame</InlineCode>,{" "}
          <InlineCode>create_changeset</InlineCode> (write, delete, move, copy, mkdir, with
          optional <InlineCode>submit: true</InlineCode>), <InlineCode>get_changeset</InlineCode>,
          and <InlineCode>submit_changeset</InlineCode>. Blocked submits return structured reasons
          (pending checks, missing approvals, or conflict paths).
        </p>
      </DocSectionCard>

      <DocSectionCard title="Run CI checks on your machine">
        <p>
          A CI-only daemon on any machine with Docker or Podman runs a slice&apos;s checks and
          reports results back to the server. Use <InlineCode>gs agent start --ci</InlineCode> in
          an empty directory to test interactively, or install it as a background user service with{" "}
          <InlineCode>gs agent install --ci</InlineCode>. See{" "}
          <DocLink section="checks">CI Checks</DocLink> for full setup and backup pools.
        </p>
        <CommandBlock label="CI daemon quickstart commands">{`mkdir my-ci && cd my-ci
gs agent start --ci`}</CommandBlock>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Set up folder-scoped CI checks, Docker builds, services, and CI daemons."
          section="checks"
          title="CI Checks"
        />
        <NextDocCard
          description="Look up gs auth, gs agent, gs intent, gs mcp, and all daily commands."
          section="cli"
          title="CLI Reference"
        />
      </section>
    </div>
  );
}
