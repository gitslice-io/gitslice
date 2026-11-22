import { Link } from "@tanstack/react-router";

import type { FolderOwnerRule, FolderPolicyProblem } from "../../../api/types";
import { SliceNotice } from "../../../components/slices/SlicePageParts";
import { InheritanceBadge, ProblemNotices } from "./PolicyShared";

export interface PolicyOwnersTabProps {
  account: string;
  ciDaemonIds: string[];
  ciDaemonNames: string[];
  folder: string;
  ownerConfigPath?: string;
  ownerRules: FolderOwnerRule[];
  ownerSourceFolder?: string;
  problems: FolderPolicyProblem[];
  sliceCIDaemonNames?: string[];
  sliceRouteParams?: { account: string; slice: string } | null;
}

export function PolicyOwnersTab({
  account,
  ciDaemonIds,
  ciDaemonNames,
  folder,
  ownerConfigPath,
  ownerRules,
  ownerSourceFolder,
  problems,
  sliceCIDaemonNames,
  sliceRouteParams
}: PolicyOwnersTabProps) {
  return (
    <div className="grid gap-5 px-4 py-4">
      <ProblemNotices problems={problems} />

      <section aria-label="Path owners">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
            Path owners
          </h3>
          {ownerSourceFolder ? (
            <InheritanceBadge currentFolder={folder} sourceFolder={ownerSourceFolder} />
          ) : null}
        </div>
        {ownerConfigPath ? (
          <p className="mb-2 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
            {ownerConfigPath}
          </p>
        ) : null}
        {ownerRules.length === 0 ? (
          <SliceNotice title="No owner rules apply here.">
            Add a <code>.gitslice/owners.yaml</code> to this folder or an ancestor folder to require owner or
            human approvals on matching paths.
          </SliceNotice>
        ) : (
          <ul className="divide-y divide-slate-200 overflow-hidden rounded-md border border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
            {ownerRules.map((rule, idx) => (
              <OwnerRuleRow key={idx} rule={rule} />
            ))}
          </ul>
        )}
      </section>

      <section aria-label="CI runners">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
            CI runners
          </h3>
          {account ? (
            <Link
              className="text-xs font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
              params={{ account }}
              to="/accounts/$account"
            >
              Account settings
            </Link>
          ) : null}
        </div>
        {sliceCIDaemonNames && sliceCIDaemonNames.length > 0 ? (
          <div className="rounded-md border border-slate-200 px-3 py-2.5 text-xs dark:border-zinc-800">
            <p className="font-semibold text-zinc-950 dark:text-zinc-50">
              Slice override: {sliceCIDaemonNames.join(", ")}
            </p>
            <p className="mt-1 text-slate-600 dark:text-zinc-400">
              This slice overrides the account CI runner pool.{" "}
              {sliceRouteParams ? (
                <Link
                  className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                  params={sliceRouteParams}
                  to="/slices/$account/$slice/settings"
                >
                  Slice settings
                </Link>
              ) : null}
            </p>
          </div>
        ) : ciDaemonIds.length > 0 ? (
          <div className="rounded-md border border-slate-200 px-3 py-2.5 text-xs dark:border-zinc-800">
            <p className="text-slate-600 dark:text-zinc-400">
              Inherited from <span className="font-mono">/{account}</span> default CI runner pool:
            </p>
            <ul className="mt-2 grid gap-1">
              {ciDaemonIds.map((id, i) => (
                <li className="flex items-center justify-between gap-2" key={id}>
                  <span className="font-medium text-zinc-950 dark:text-zinc-50">
                    {ciDaemonNames[i] || id}
                  </span>
                  <span className="font-mono text-[11px] text-slate-500 dark:text-zinc-400">{id}</span>
                </li>
              ))}
            </ul>
          </div>
        ) : (
          <SliceNotice title="No account CI runners configured.">
            Configure a default CI runner pool on{" "}
            {account ? (
              <Link
                className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                params={{ account }}
                to="/accounts/$account"
              >
                /{account}
              </Link>
            ) : (
              "the account page"
            )}{" "}
            so checks in every slice run automatically.
          </SliceNotice>
        )}
      </section>
    </div>
  );
}

function OwnerRuleRow({ rule }: { rule: FolderOwnerRule }) {
  const parts: string[] = [];
  if ((rule.require ?? 0) > 0) {
    parts.push(`${rule.require} approval${rule.require === 1 ? "" : "s"}`);
  }
  if ((rule.requireHuman ?? 0) > 0) {
    parts.push(`${rule.requireHuman} human`);
  }
  return (
    <li className="px-3 py-2.5 text-xs">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="break-all font-mono font-semibold text-zinc-950 dark:text-zinc-50">
          {(rule.paths ?? []).join(", ")}
        </span>
        {parts.length > 0 ? (
          <span className="shrink-0 rounded border border-slate-200 bg-slate-50 px-1.5 py-px text-[11px] font-medium text-slate-700 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-300">
            {parts.join(" · ")}
          </span>
        ) : null}
      </div>
      <p className="mt-1 break-all text-slate-600 dark:text-zinc-400">
        Owners: <span className="font-mono">{(rule.owners ?? []).join(", ")}</span>
      </p>
    </li>
  );
}
