import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { AgentInvite, CreateAgentInviteResponse, SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { useAppAuth } from "../../auth/AppAuthProvider";
import { formatDate } from "../../lib/time";
import { agentInvitesKey, agentInvitesQuery } from "../../queries/auth";
import { Button } from "../ui/Button";
import { CopyButton } from "../ui/CopyButton";
import { SlicePanel, getErrorMessage } from "./SlicePageParts";

type SnippetTab = "prompt" | "cli" | "mcp";

const EXPIRY_OPTIONS: Array<{ label: string; seconds: number }> = [
  { label: "1 hour", seconds: 3600 },
  { label: "24 hours", seconds: 86400 },
  { label: "7 days", seconds: 604800 }
];

const ROLE_OPTIONS = ["contributor", "writer", "reader", "admin"] as const;

export function ConnectAgentPanel({
  slice,
  title = "Connect an agent",
  description
}: {
  slice?: SliceRef;
  title?: string;
  description?: string;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { authKind, isLoaded, isSignedIn } = useAppAuth();
  const enabled = Boolean(isLoaded && isSignedIn && authKind === "clerk");

  const [role, setRole] = useState<string>("contributor");
  const [suggestedUsername, setSuggestedUsername] = useState("");
  const [expiresInSeconds, setExpiresInSeconds] = useState<number>(86400);
  const [created, setCreated] = useState<CreateAgentInviteResponse | null>(null);
  const [activeTab, setActiveTab] = useState<SnippetTab>("prompt");

  const invitesQuery = useQuery({
    ...agentInvitesQuery(api),
    enabled
  });

  const createMutation = useMutation({
    mutationFn: () =>
      api.createAgentInvite({
        slice: slice ? { account: slice.account, slice: slice.slice } : undefined,
        role: slice ? role : undefined,
        suggestedUsername: suggestedUsername.trim() || undefined,
        expiresInSeconds: String(expiresInSeconds)
      }),
    onSuccess: async (resp) => {
      setCreated(resp);
      setActiveTab("prompt");
      await queryClient.invalidateQueries({ queryKey: agentInvitesKey });
    }
  });

  const revokeMutation = useMutation({
    mutationFn: (inviteId: string) => api.revokeAgentInvite({ inviteId }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: agentInvitesKey });
    }
  });

  if (!enabled) {
    return null;
  }

  const sliceLabel = slice?.account && slice?.slice ? `${slice.account}/${slice.slice}` : "";
  const defaultDesc = sliceLabel
    ? `Mint a single-use join token so a coding agent can install gs, register under your ownership, and immediately get ${role} access to ${sliceLabel}.`
    : "Mint a single-use join token so a coding agent can register and link directly to your account in one command.";

  const activeInvites = (invitesQuery.data ?? []).filter((inv) => {
    if (inv.consumedAt || inv.revokedAt) {
      return false;
    }
    if (sliceLabel) {
      return inv.slice?.account === slice?.account && inv.slice?.slice === slice?.slice;
    }
    return true;
  });

  const token = created?.token ?? "";
  const snippets = token ? buildSnippets(token, sliceLabel, created?.invite?.suggestedUsername ?? "") : null;

  return (
    <SlicePanel>
      <div className="space-y-4">
        <div>
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">{title}</h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            {description ?? defaultDesc}
          </p>
        </div>

        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            createMutation.mutate();
          }}
        >
          {sliceLabel ? (
            <label className="grid gap-1 text-xs font-medium text-slate-700 dark:text-zinc-300">
              <span>Slice role</span>
              <select
                aria-label="Slice role"
                className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-zinc-950 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50"
                onChange={(e) => setRole(e.target.value)}
                value={role}
              >
                {ROLE_OPTIONS.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
            </label>
          ) : null}

          <label className="grid min-w-[12rem] flex-1 gap-1 text-xs font-medium text-slate-700 dark:text-zinc-300">
            <span>Suggested username (optional)</span>
            <input
              aria-label="Suggested username"
              className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-zinc-950 placeholder:text-slate-400 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:placeholder:text-zinc-500"
              onChange={(e) => setSuggestedUsername(e.target.value)}
              placeholder="e.g. triage-bot"
              type="text"
              value={suggestedUsername}
            />
          </label>

          <label className="grid gap-1 text-xs font-medium text-slate-700 dark:text-zinc-300">
            <span>Expires in</span>
            <select
              aria-label="Invite expiration"
              className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-zinc-950 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50"
              onChange={(e) => setExpiresInSeconds(Number(e.target.value))}
              value={expiresInSeconds}
            >
              {EXPIRY_OPTIONS.map((opt) => (
                <option key={opt.seconds} value={opt.seconds}>
                  {opt.label}
                </option>
              ))}
            </select>
          </label>

          <Button disabled={createMutation.isPending} type="submit" variant="primary">
            {createMutation.isPending ? "Generating..." : "Generate join token"}
          </Button>
        </form>

        {createMutation.isError ? (
          <p className="text-sm text-rose-700 dark:text-rose-300" role="alert">
            {getErrorMessage(createMutation.error)}
          </p>
        ) : null}

        {created && snippets ? (
          <div className="rounded-md border border-emerald-200 bg-emerald-50/70 p-3.5 dark:border-emerald-900/60 dark:bg-emerald-950/20">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-xs font-medium text-emerald-900 dark:text-emerald-200">
                Single-use join token created (shown once
                {created.invite?.expiresAt
                  ? ` · expires ${formatDate(new Date(created.invite.expiresAt))}`
                  : ""}
                ).
              </p>
              <div className="flex items-center gap-1" role="tablist">
                {(
                  [
                    ["prompt", "Agent prompt"],
                    ["cli", "CLI command"],
                    ["mcp", "Hosted MCP"]
                  ] as const
                ).map(([id, label]) => (
                  <button
                    aria-selected={activeTab === id}
                    className={
                      activeTab === id
                        ? "rounded px-2 py-1 text-xs font-semibold bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-950"
                        : "rounded px-2 py-1 text-xs font-medium text-slate-600 hover:bg-slate-200/70 dark:text-zinc-400 dark:hover:bg-zinc-800"
                    }
                    key={id}
                    onClick={() => setActiveTab(id)}
                    role="tab"
                    type="button"
                  >
                    {label}
                  </button>
                ))}
              </div>
            </div>

            <pre className="mt-2.5 overflow-x-auto rounded border border-slate-200 bg-white p-3 font-mono text-xs leading-5 text-zinc-900 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-100">
              <code>{snippets[activeTab]}</code>
            </pre>

            <div className="mt-2 flex items-center justify-between gap-2">
              <p className="text-xs text-slate-600 dark:text-zinc-400">
                {activeTab === "mcp"
                  ? "After joining, the agent uses its saved API key (gs auth token) with the hosted MCP endpoint."
                  : "Paste this into Claude Code, Codex, Cursor, Gemini, or any coding agent."}
              </p>
              <CopyButton
                key={`${token}-${activeTab}`}
                size="sm"
                value={snippets[activeTab]}
                variant="secondary"
              />
            </div>
          </div>
        ) : null}

        {activeInvites.length > 0 ? (
          <div className="pt-2">
            <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
              Active invites ({activeInvites.length})
            </h3>
            <ul className="mt-2 grid gap-2">
              {activeInvites.map((inv) => (
                <InviteRow
                  invite={inv}
                  isRevoking={
                    revokeMutation.isPending && revokeMutation.variables === inv.id
                  }
                  key={inv.id}
                  onRevoke={() => {
                    if (inv.id) {
                      revokeMutation.mutate(inv.id);
                    }
                  }}
                />
              ))}
            </ul>
          </div>
        ) : null}
      </div>
    </SlicePanel>
  );
}

function InviteRow({
  invite,
  isRevoking,
  onRevoke
}: {
  invite: AgentInvite;
  isRevoking: boolean;
  onRevoke: () => void;
}) {
  const sliceText =
    invite.slice?.account && invite.slice?.slice
      ? `${invite.slice.account}/${invite.slice.slice} (${invite.role || "contributor"})`
      : "Account-only";
  const expires = invite.expiresAt ? new Date(invite.expiresAt) : null;

  return (
    <li className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-slate-200 bg-white px-3 py-2 text-xs dark:border-zinc-800 dark:bg-zinc-900">
      <div className="min-w-0">
        <p className="truncate font-mono text-zinc-900 dark:text-zinc-100">
          {invite.id}
          {invite.suggestedUsername ? ` · @${invite.suggestedUsername}` : ""}
        </p>
        <p className="truncate text-slate-500 dark:text-zinc-400">
          {sliceText}
          {expires && !Number.isNaN(expires.getTime())
            ? ` · expires ${formatDate(expires)}`
            : ""}
        </p>
      </div>
      <Button disabled={isRevoking} onClick={onRevoke} variant="secondary">
        {isRevoking ? "Revoking..." : "Revoke"}
      </Button>
    </li>
  );
}

function buildSnippets(
  token: string,
  sliceLabel: string,
  suggestedUsername: string
): Record<SnippetTab, string> {
  const userFlag = suggestedUsername ? ` --username ${suggestedUsername}` : "";
  const initLine = sliceLabel ? `\ngs init ${sliceLabel}` : "";
  const cli = `gs onboard --join ${token}${userFlag} --install-skill --json${initLine}`;
  const prompt = [
    `Connect to Gitslice${sliceLabel ? ` for slice ${sliceLabel}` : ""} using this single-use invite token:`,
    `1. Install the gs CLI and Agent Skill if not installed:`,
    `   curl -fsSL https://gitslice.io/install.sh | GS_INSTALL_SKILL=1 sh`,
    `2. Join and configure your workspace:`,
    `   ${cli}`,
    `Read https://gitslice.io/SKILL.md for the full Gitslice workflow.`
  ].join("\n");

  const mcpUrl = sliceLabel
    ? `https://gitslice.io/mcp?slice=${encodeURIComponent(sliceLabel)}`
    : "https://gitslice.io/mcp";
  const mcp = JSON.stringify(
    {
      mcpServers: {
        gitslice: {
          url: mcpUrl,
          headers: {
            Authorization: "Bearer <agent-api-key-from-gs-auth-token>"
          }
        }
      }
    },
    null,
    2
  );

  return { prompt, cli, mcp };
}
