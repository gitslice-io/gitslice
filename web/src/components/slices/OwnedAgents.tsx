import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { ownedAgentsQuery } from "../../api/queries";
import type { OwnedAgent } from "../../api/types";
import { useApi } from "../../api/useApi";
import { formatRelativeTime } from "./ConversationCard";

// useOwnedAgents returns the self-registered agents whose accounts the
// signed-in user owns. Errors resolve to an empty list so callers (the home
// slice list) degrade to showing only the user's own account.
export function useOwnedAgents() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const enabled = Boolean(isLoaded && isSignedIn);
  const query = useQuery({ ...ownedAgentsQuery(api), enabled });
  return {
    agents: query.isError ? [] : (query.data ?? []),
    enabled,
    isError: query.isError,
    isLoading: enabled && query.isPending
  };
}

// OwnedAgents is the home page "Your agents" section: the agents you co-own
// after claiming them, with when each last used its API key.
export function OwnedAgents() {
  const { agents, enabled, isError, isLoading } = useOwnedAgents();
  // On error, render nothing rather than a misleading "No agents yet".
  if (!enabled || isLoading || isError) {
    return null;
  }

  return (
    <section aria-labelledby="owned-agents-heading">
      <div className="flex items-end justify-between gap-3">
        <div className="min-w-0">
          <h2
            className="text-sm font-semibold text-zinc-950 dark:text-zinc-50"
            id="owned-agents-heading"
          >
            Your agents
          </h2>
          <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
            Agents that signed themselves up with your email and that you
            co-own.
          </p>
        </div>
      </div>

      <div className="mt-3">
        {agents.length === 0 ? (
          <div className="rounded-md border border-dashed border-slate-300 p-4 text-sm leading-6 text-slate-600 dark:border-zinc-700 dark:text-zinc-400">
            No agents yet. Point an agent at{" "}
            <a
              className="font-medium text-zinc-950 underline underline-offset-2 dark:text-zinc-50"
              href="/llms.txt"
            >
              gitslice.io/llms.txt
            </a>{" "}
            to have it sign up with your email, then accept it here.
          </div>
        ) : (
          <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {agents.map((agent) => (
              <OwnedAgentCard agent={agent} key={agent.agentSubjectId} />
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

function OwnedAgentCard({ agent }: { agent: OwnedAgent }) {
  const account = agent.account ?? "";
  const name = agent.agentDisplayName || account;
  return (
    <li className="rounded-md border border-slate-200 bg-white px-3 py-2 shadow-sm dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex items-center justify-between gap-2">
        <p className="min-w-0 truncate text-sm font-medium text-zinc-950 dark:text-zinc-50">
          {name}
        </p>
        <span className="shrink-0 rounded-full border border-sky-200 bg-sky-50 px-2 py-0.5 text-[11px] font-medium text-sky-800 dark:border-sky-900/60 dark:bg-sky-950/40 dark:text-sky-300">
          agent
        </span>
      </div>
      <p className="mt-1 truncate text-xs text-slate-500 dark:text-zinc-400">
        {account ? (
          <Link
            className="underline decoration-slate-300 underline-offset-2 hover:decoration-slate-700"
            params={{ account, slice: "home" }}
            to="/slices/$account/$slice"
          >
            {account}/home
          </Link>
        ) : null}
        {" · "}
        {agent.lastActiveAt
          ? `active ${formatRelativeTime(agent.lastActiveAt)}`
          : "never active"}
      </p>
      <p className="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
        {agent.claimedAt
          ? `Claimed ${formatRelativeTime(agent.claimedAt)}`
          : `Registered ${formatRelativeTime(agent.registeredAt)}`}
      </p>
    </li>
  );
}
