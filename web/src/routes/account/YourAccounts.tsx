import { Link } from "@tanstack/react-router";

import { AccountAvatar } from "../../components/AccountAvatar";
import { AgentDaemons } from "../../components/slices/AgentDaemons";
import { OwnedAgents } from "../../components/slices/OwnedAgents";
import { buttonClassName } from "../../components/ui/Button";
import type { Membership } from "../../lib/accounts";

// On your own page: the organizations you belong to, with your role, like a
// GitHub profile's organizations, and the agents you have claimed.
export function YourAccounts({ memberships }: { memberships: readonly Membership[] }) {
  const groups = [
    { kind: "organization", title: "Organizations", empty: "You do not belong to any organizations yet." }
  ];
  return (
    <>
      {groups.map(({ empty, kind, title }) => {
        const accounts = memberships.filter((m) => m.kind === kind);
        if (accounts.length === 0 && !empty) {
          return null;
        }
        return (
          <section aria-label={title} className="mt-10" key={kind}>
            <div className="mb-2 flex items-center justify-between gap-3">
              <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">{title}</h2>
              {kind === "organization" ? (
                <Link className={buttonClassName({ size: "sm", variant: "secondary" })} to="/organizations/new">
                  New organization
                </Link>
              ) : null}
            </div>
            {accounts.length === 0 ? (
              <p className="text-sm text-slate-600 dark:text-zinc-400">{empty}</p>
            ) : (
              <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900">
                {accounts.map((m) => (
                  <li key={m.account}>
                    <Link
                      className="flex items-center gap-3 px-4 py-3 transition hover:bg-slate-50 dark:hover:bg-zinc-950"
                      params={{ account: m.account }}
                      to="/accounts/$account"
                    >
                      <AccountAvatar account={m.account} kind={m.kind} />
                      <span className="min-w-0 flex-1 truncate text-sm font-medium text-zinc-950 dark:text-zinc-50">
                        {m.account}
                      </span>
                      {m.role ? <span className="text-xs text-slate-500 dark:text-zinc-400">{m.role}</span> : null}
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>
        );
      })}
      <div className="mt-10">
        <OwnedAgents />
      </div>
      <div className="mt-10">
        <AgentDaemons />
      </div>
    </>
  );
}
