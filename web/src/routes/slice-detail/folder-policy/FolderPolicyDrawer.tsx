import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";

import type { ApiClient } from "../../../api/useApi";
import { scopeFromDraft, type SecretDraft } from "../../../components/secrets/SecretForm";
import { SliceNotice } from "../../../components/slices/SlicePageParts";
import { Drawer } from "../../../components/ui/Drawer";
import { cn } from "../../../lib/cn";
import { folderPolicyKey, folderPolicyQuery } from "../../../queries/automations";
import { secretsKey } from "../../../queries/secrets";
import { PolicyAutomationsTab } from "./PolicyAutomationsTab";
import { PolicyOwnersTab } from "./PolicyOwnersTab";
import { PolicySecretsTab } from "./PolicySecretsTab";

type PolicyTab = "automation" | "secrets" | "owners";

const TAB_ORDER: PolicyTab[] = ["automation", "secrets", "owners"];

export interface FolderPolicyDrawerProps {
  api: ApiClient;
  // Canonical folder shown, such as /acme or /acme/services/billing.
  folder: string;
  onClose(): void;
  open: boolean;
  // Optional slice-level CI override names so the Owners & Runners tab can
  // clarify when this slice view overrides the account default pool.
  sliceCIDaemonNames?: string[];
  sliceRouteParams?: { account: string; slice: string } | null;
}

export { folderPolicyKey as folderPolicyQueryKey };

// Unified Folder Inspector Drawer showing everything bound to a folder at the
// head of main: triggers, checks (with required gates), secrets (with
// inheritance badges and inline creation for admins), path owners, and the
// account's default CI runner pool.
export function FolderPolicyDrawer({
  api,
  folder,
  onClose,
  open,
  sliceCIDaemonNames,
  sliceRouteParams
}: FolderPolicyDrawerProps) {
  const queryClient = useQueryClient();
  const tabRefs = useRef<Record<PolicyTab, HTMLButtonElement | null>>({
    automation: null,
    secrets: null,
    owners: null
  });
  const baseId = useId();
  const [activeTab, setActiveTab] = useState<PolicyTab>("automation");
  const [addingSecret, setAddingSecret] = useState(false);
  const [secretError, setSecretError] = useState("");

  useEffect(() => {
    setAddingSecret(false);
    setSecretError("");
  }, [folder, open]);

  const policyQuery = useQuery({
    ...folderPolicyQuery(api, folder),
    enabled: open && Boolean(folder)
  });

  const policy = policyQuery.data;
  const account = policy?.account || folder.split("/").filter(Boolean)[0] || "";
  const checks = policy?.checks ?? [];
  const triggers = policy?.automations ?? [];
  const secrets = policy?.secrets ?? [];
  const ownerRules = policy?.ownerRules ?? [];
  const problems = policy?.problems ?? [];
  const ciDaemonIds = policy?.ciDaemonIds ?? [];
  const ciDaemonNames = policy?.ciDaemonNames ?? [];

  const createSecret = useMutation({
    mutationFn: (draft: SecretDraft) =>
      api.createSecret({
        account,
        name: draft.name,
        scope: scopeFromDraft(draft),
        value: draft.value
      }),
    onMutate: () => setSecretError(""),
    onError: (err) => setSecretError(err instanceof Error ? err.message : String(err)),
    onSuccess: async () => {
      setAddingSecret(false);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: folderPolicyKey(folder) }),
        queryClient.invalidateQueries({ queryKey: secretsKey(account) })
      ]);
    }
  });

  function selectTab(next: PolicyTab, focusTab = false) {
    setActiveTab(next);
    if (focusTab) {
      tabRefs.current[next]?.focus();
      tabRefs.current[next]?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    }
  }

  function handleTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, current: PolicyTab) {
    const idx = TAB_ORDER.indexOf(current);
    let nextIdx = -1;
    if (event.key === "ArrowRight") {
      nextIdx = (idx + 1) % TAB_ORDER.length;
    } else if (event.key === "ArrowLeft") {
      nextIdx = (idx - 1 + TAB_ORDER.length) % TAB_ORDER.length;
    } else if (event.key === "Home") {
      nextIdx = 0;
    } else if (event.key === "End") {
      nextIdx = TAB_ORDER.length - 1;
    }
    if (nextIdx >= 0) {
      event.preventDefault();
      selectTab(TAB_ORDER[nextIdx], true);
    }
  }

  const automationCount = triggers.length + checks.length;
  const tabMeta: { id: PolicyTab; label: string; count?: number }[] = [
    { id: "automation", label: "Automation", count: policy ? automationCount : undefined },
    { id: "secrets", label: "Secrets", count: policy ? secrets.length : undefined },
    { id: "owners", label: "Owners & Runners", count: policy ? ownerRules.length : undefined }
  ];

  const automationProblems = problems.filter(
    (p) => p.kind === "checks" || p.kind === "triggers" || p.kind === "automations"
  );
  const ownerProblems = problems.filter((p) => p.kind === "owners");

  return (
    <Drawer
      closeLabel="Close folder policy"
      onClose={onClose}
      open={open}
      size="xl"
      subtitle={
        <>
          <p className="mt-0.5 break-all font-mono text-xs text-slate-600 dark:text-zinc-300">{folder}</p>
          <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
            Effective rules, checks, automations, and secrets for this folder at the head of main.
          </p>
        </>
      }
      title="Folder policy"
    >
      <div
        aria-label="Folder policy sections"
        className="flex shrink-0 overflow-x-auto border-b border-slate-200 px-4 dark:border-zinc-800"
        role="tablist"
      >
        {tabMeta.map((tab) => {
          const selected = activeTab === tab.id;
          return (
            <button
              aria-selected={selected}
              className={cn(
                "relative flex-none whitespace-nowrap px-3 py-2.5 text-xs font-semibold transition",
                "text-slate-600 hover:text-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-100",
                "aria-selected:text-zinc-950 dark:aria-selected:text-zinc-50",
                "aria-selected:after:absolute aria-selected:after:inset-x-0 aria-selected:after:bottom-0 aria-selected:after:border-b-2 aria-selected:after:border-zinc-950 dark:aria-selected:after:border-zinc-100"
              )}
              id={`${baseId}-tab-${tab.id}`}
              key={tab.id}
              onClick={() => selectTab(tab.id)}
              onKeyDown={(event) => handleTabKeyDown(event, tab.id)}
              ref={(node) => {
                tabRefs.current[tab.id] = node;
              }}
              role="tab"
              tabIndex={selected ? 0 : -1}
              type="button"
            >
              {tab.label}
              {tab.count !== undefined ? (
                <span className="ml-1.5 rounded-full bg-slate-100 px-1.5 py-px text-[11px] font-medium text-slate-600 dark:bg-zinc-800 dark:text-zinc-300">
                  {tab.count}
                </span>
              ) : null}
            </button>
          );
        })}
      </div>

      <div className="flex-1 overflow-y-auto overflow-x-hidden pb-[max(1rem,env(safe-area-inset-bottom,0px))]">
        {policyQuery.isPending ? (
          <div className="grid gap-2 px-4 py-4">
            <div className="h-16 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
            <div className="h-16 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
          </div>
        ) : policyQuery.isError ? (
          <div className="px-4 py-4">
            <SliceNotice title="Could not load folder policy" tone="error">
              {policyQuery.error?.message}
            </SliceNotice>
          </div>
        ) : (
          <>
            <div
              aria-labelledby={`${baseId}-tab-automation`}
              className="min-w-0"
              hidden={activeTab !== "automation"}
              role="tabpanel"
            >
              <PolicyAutomationsTab
                account={account}
                api={api}
                checks={checks}
                folder={folder}
                problems={automationProblems}
                triggers={triggers}
              />
            </div>

            <div
              aria-labelledby={`${baseId}-tab-secrets`}
              hidden={activeTab !== "secrets"}
              role="tabpanel"
            >
              <PolicySecretsTab
                account={account}
                addingSecret={addingSecret}
                canManageSecrets={Boolean(policy?.canManageSecrets)}
                folder={folder}
                isPending={createSecret.isPending}
                onCancelAdd={() => {
                  setAddingSecret(false);
                  setSecretError("");
                }}
                onStartAdd={() => setAddingSecret(true)}
                onSubmitSecret={(draft) => createSecret.mutate(draft)}
                secretError={secretError}
                secrets={secrets}
              />
            </div>

            <div
              aria-labelledby={`${baseId}-tab-owners`}
              hidden={activeTab !== "owners"}
              role="tabpanel"
            >
              <PolicyOwnersTab
                account={account}
                ciDaemonIds={ciDaemonIds}
                ciDaemonNames={ciDaemonNames}
                folder={folder}
                ownerConfigPath={policy?.ownerConfigPath}
                ownerRules={ownerRules}
                ownerSourceFolder={policy?.ownerSourceFolder}
                problems={ownerProblems}
                sliceCIDaemonNames={sliceCIDaemonNames}
                sliceRouteParams={sliceRouteParams}
              />
            </div>
          </>
        )}
      </div>
    </Drawer>
  );
}
