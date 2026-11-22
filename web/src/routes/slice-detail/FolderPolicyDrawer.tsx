import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";

import type {
  FolderCheckDefinition,
  FolderOwnerRule,
  FolderPolicyProblem,
  FolderSecretEntry,
  AutomationDefinition,
  AutomationRun
} from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { SecretForm, scopeFromDraft, type SecretDraft } from "../../components/secrets/SecretForm";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { Timestamp } from "../../components/Timestamp";
import { Button } from "../../components/ui/Button";
import { cn } from "../../lib/cn";
import { shortChangesetId } from "../../lib/objectId";
import { secretsKey } from "../../queries/secrets";
import { AutomationRunLog, isFinalTriggerStatus } from "../../components/automations/AutomationRunLog";
import { needsRunModal, RunAutomationModal } from "./RunAutomationModal";

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

export function folderPolicyQueryKey(folder: string) {
  return ["folderPolicy", folder] as const;
}

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
  const dialogRef = useRef<HTMLDialogElement>(null);
  const sheetRef = useRef<HTMLDivElement>(null);
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

  // Manage native modal <dialog> open/close with a safe fallback when showModal
  // is unavailable (e.g. jsdom in unit tests).
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) {
      return;
    }
    if (open) {
      if (!dialog.open) {
        if (typeof dialog.showModal === "function") {
          dialog.showModal();
        } else {
          dialog.setAttribute("open", "");
        }
      }
    } else if (dialog.open || dialog.hasAttribute("open")) {
      if (typeof dialog.close === "function") {
        dialog.close();
      } else {
        dialog.removeAttribute("open");
      }
    }
  }, [open]);

  const policyQuery = useQuery({
    enabled: open && Boolean(folder),
    queryKey: folderPolicyQueryKey(folder),
    queryFn: () => api.getFolderPolicy({ path: folder })
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
        queryClient.invalidateQueries({ queryKey: folderPolicyQueryKey(folder) }),
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

  if (!open) {
    return null;
  }

  return (
    <dialog
      aria-labelledby={`${baseId}-title`}
      className="fixed inset-0 z-50 m-0 h-dvh max-h-none w-screen max-w-none border-0 bg-transparent p-0 backdrop:bg-black/35"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (sheetRef.current && !sheetRef.current.contains(event.target as Node)) {
          onClose();
        }
      }}
      ref={dialogRef}
    >
      <div className="flex h-full w-full justify-end">
        <div
          className="flex h-full w-full max-w-xl min-w-0 flex-col bg-white text-zinc-950 outline-none shadow-xl dark:bg-zinc-900 dark:text-zinc-50"
          ref={sheetRef}
          tabIndex={-1}
        >
          <div className="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <h2 className="text-base font-semibold tracking-tight text-zinc-950 dark:text-zinc-50" id={`${baseId}-title`}>
                  Folder policy
                </h2>
                <p className="mt-0.5 break-all font-mono text-xs text-slate-600 dark:text-zinc-300">{folder}</p>
                <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                  Effective rules, checks, automations, and secrets for this folder at the head of main.
                </p>
              </div>
              <button
                aria-label="Close folder policy"
                className="inline-flex min-h-9 min-w-9 shrink-0 items-center justify-center rounded-md text-slate-500 transition hover:bg-slate-100 hover:text-zinc-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 active:scale-[0.98] dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50 [@media(pointer:coarse)]:min-h-11 [@media(pointer:coarse)]:min-w-11"
                onClick={onClose}
                type="button"
              >
                <svg
                  aria-hidden="true"
                  className="h-4 w-4"
                  fill="none"
                  stroke="currentColor"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth="2"
                  viewBox="0 0 24 24"
                >
                  <path d="M18 6 6 18" />
                  <path d="m6 6 12 12" />
                </svg>
              </button>
            </div>
          </div>

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
                  <div className="grid min-w-0 gap-5 px-4 py-4">
                    <ProblemNotices problems={automationProblems} />

                    <section aria-label="Checks" className="min-w-0">
                      <div className="mb-2 flex items-center justify-between gap-2">
                        <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
                          Checks
                        </h3>
                        <Link
                          className="shrink-0 text-xs font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                          params={{ section: "checks" } as never}
                          to="/doc/$section"
                        >
                          checks.yaml docs
                        </Link>
                      </div>
                      {checks.length === 0 ? (
                        <SliceNotice title="No checks apply here.">
                          Add a <code>.gitslice/checks.yaml</code> to this folder or one above it. Mark a check{" "}
                          <code>required: true</code> to gate submissions touching its paths.
                        </SliceNotice>
                      ) : (
                        <div className="grid min-w-0 gap-2">
                          {checks.map((check) => (
                            <CheckCard check={check} folder={folder} key={check.id} />
                          ))}
                        </div>
                      )}
                    </section>

                    <section aria-label="Automations" className="min-w-0">
                      <div className="mb-2 flex items-center justify-between gap-2">
                        <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
                          Automations
                        </h3>
                        <Link
                          className="text-xs font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                          params={{ section: "automations" } as never}
                          to="/doc/$section"
                        >
                          automations.yaml docs
                        </Link>
                      </div>
                      {triggers.length === 0 ? (
                        <SliceNotice title="No automations apply here.">
                          Add a <code>.gitslice/automations.yaml</code> to this folder or one above it.
                        </SliceNotice>
                      ) : (
                        <div className="grid gap-3">
                          {triggers.map((trigger) => (
                            <TriggerCard account={account} api={api} folder={folder} key={trigger.id} trigger={trigger} />
                          ))}
                        </div>
                      )}
                    </section>
                  </div>
                </div>

                <div
                  aria-labelledby={`${baseId}-tab-secrets`}
                  hidden={activeTab !== "secrets"}
                  role="tabpanel"
                >
                  <div className="grid gap-4 px-4 py-4">
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <p className="max-w-md text-xs leading-5 text-slate-600 dark:text-zinc-400">
                        Secrets defined on <span className="font-mono">{folder}</span> or inherited from an ancestor folder are
                        injected into checks and automations here.
                      </p>
                      {policy?.canManageSecrets && !addingSecret ? (
                        <Button
                          onClick={() => setAddingSecret(true)}
                          size="sm"
                          variant="primary"
                        >
                          Add secret
                        </Button>
                      ) : null}
                    </div>

                    {secretError ? (
                      <SliceNotice title="Could not save secret" tone="error">
                        {secretError}
                      </SliceNotice>
                    ) : null}

                    {policy?.canManageSecrets && addingSecret ? (
                      <SecretForm
                        account={account}
                        busy={createSecret.isPending}
                        initial={{ name: "", paths: [folder], value: "" }}
                        onCancel={() => {
                          setAddingSecret(false);
                          setSecretError("");
                        }}
                        onSubmit={(draft) => createSecret.mutate(draft)}
                        submitLabel="Add secret"
                      />
                    ) : null}

                    {secrets.length === 0 ? (
                      <SliceNotice title="No secrets apply here.">
                        {policy?.canManageSecrets
                          ? `Add a secret scoped to ${folder} (or /${account} for the whole monorepo).`
                          : "Only writers and admins of this folder see secret names. Values are never shown in the UI."}
                      </SliceNotice>
                    ) : (
                      <ul className="divide-y divide-slate-200 overflow-hidden rounded-md border border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
                        {secrets.map((secret) => (
                          <SecretRow folder={folder} key={secret.name} secret={secret} />
                        ))}
                      </ul>
                    )}

                    {policy?.canManageSecrets && account ? (
                      <p className="text-xs text-slate-500 dark:text-zinc-400">
                        Manage, rotate, or delete secrets across folders on{" "}
                        <Link
                          className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                          params={{ account }}
                          to="/accounts/$account/secrets"
                        >
                          {account}'s Secrets page
                        </Link>
                        .
                      </p>
                    ) : null}
                  </div>
                </div>

                <div
                  aria-labelledby={`${baseId}-tab-owners`}
                  hidden={activeTab !== "owners"}
                  role="tabpanel"
                >
                  <div className="grid gap-5 px-4 py-4">
                    <ProblemNotices problems={ownerProblems} />

                    <section aria-label="Path owners">
                      <div className="mb-2 flex items-center justify-between gap-2">
                        <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
                          Path owners
                        </h3>
                        {policy?.ownerSourceFolder ? (
                          <InheritanceBadge currentFolder={folder} sourceFolder={policy.ownerSourceFolder} />
                        ) : null}
                      </div>
                      {policy?.ownerConfigPath ? (
                        <p className="mb-2 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
                          {policy.ownerConfigPath}
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
                </div>
              </>
            )}
          </div>
        </div>
      </div>
    </dialog>
  );
}

function ProblemNotices({ problems }: { problems: FolderPolicyProblem[] }) {
  if (problems.length === 0) {
    return null;
  }
  return (
    <div className="grid gap-2">
      {problems.map((p) => (
        <SliceNotice key={`${p.kind}:${p.file}`} title={`${p.file} does not parse`} tone="error">
          {p.message}
        </SliceNotice>
      ))}
    </div>
  );
}

function InheritanceBadge({ currentFolder, sourceFolder }: { currentFolder: string; sourceFolder: string }) {
  if (!sourceFolder) {
    return null;
  }
  const inherited = sourceFolder !== currentFolder;
  return (
    <span
      className={cn(
        "shrink-0 rounded border px-1.5 py-px font-mono text-[11px] font-medium",
        inherited
          ? "border-slate-200 bg-slate-50 text-slate-600 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-300"
          : "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900/60 dark:bg-emerald-950/40 dark:text-emerald-300"
      )}
    >
      {inherited ? `Inherited from ${sourceFolder}` : "Defined here"}
    </span>
  );
}

function CheckCard({ check, folder }: { check: FolderCheckDefinition; folder: string }) {
  return (
    <div className="min-w-0 rounded-md border border-slate-200 px-3 py-2.5 dark:border-zinc-800">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">{check.id}</span>
          {check.required ? (
            <span className="rounded border border-amber-300 bg-amber-50 px-1.5 py-px text-[11px] font-semibold text-amber-900 dark:border-amber-800/80 dark:bg-amber-950/50 dark:text-amber-200">
              Required
            </span>
          ) : null}
          {check.startedBy === "trigger" || check.startedBy === "automation" ? (
            <span className="rounded border border-slate-200 bg-slate-50 px-1.5 py-px text-[11px] font-medium text-slate-600 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-300">
              Automation-started
            </span>
          ) : null}
        </div>
        <InheritanceBadge currentFolder={folder} sourceFolder={check.folder ?? ""} />
      </div>
      {check.run ? (
        <pre className="mt-1.5 max-w-full overflow-x-auto whitespace-pre rounded bg-slate-50 px-2 py-1.5 font-mono text-xs text-slate-600 dark:bg-zinc-950/70 dark:text-zinc-400">
          {check.run}
        </pre>
      ) : null}
      {(check.paths ?? []).length > 0 ? (
        <p className="mt-1.5 [overflow-wrap:anywhere] font-mono text-[11px] text-slate-500 dark:text-zinc-400">
          paths {(check.paths ?? []).join(" ")}
        </p>
      ) : null}
    </div>
  );
}

function SecretRow({ folder, secret }: { folder: string; secret: FolderSecretEntry }) {
  return (
    <li className="flex flex-wrap items-center justify-between gap-2 px-3 py-2.5 text-xs">
      <div className="min-w-0">
        <p className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">{secret.name}</p>
        {(secret.paths ?? []).length > 1 ? (
          <p className="mt-0.5 break-all font-mono text-[11px] text-slate-500 dark:text-zinc-400">
            scopes: {(secret.paths ?? []).join(", ")}
          </p>
        ) : null}
      </div>
      {secret.sourceFolder ? <InheritanceBadge currentFolder={folder} sourceFolder={secret.sourceFolder} /> : null}
    </li>
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

function TriggerCard({
  account,
  api,
  folder,
  trigger
}: {
  account: string;
  api: ApiClient;
  folder: string;
  trigger: AutomationDefinition;
}) {
  const queryClient = useQueryClient();
  const [openRun, setOpenRun] = useState("");
  const [modalOpen, setModalOpen] = useState(false);
  const runsKey = ["triggerRunsOf", account, trigger.id];
  const runsQuery = useQuery({
    enabled: Boolean(account && trigger.id),
    queryKey: runsKey,
    queryFn: async () => (await api.listAutomationRuns({ account, automationId: trigger.id, limit: 5 })).runs ?? [],
    refetchInterval: (query) => (query.state.data?.some((run) => !isFinalTriggerStatus(run.status)) ? 3500 : false)
  });
  const run = useMutation({
    mutationFn: (inputs?: Record<string, string>) =>
      api.runAutomation(inputs && Object.keys(inputs).length > 0 ? { account, automationId: trigger.id, inputs } : { account, automationId: trigger.id }),
    onSuccess: (created) => {
      setModalOpen(false);
      if (created?.id) {
        setOpenRun(created.id);
      }
      return queryClient.invalidateQueries({ queryKey: runsKey });
    }
  });
  const stepCount = (trigger.steps ?? []).length;
  const what =
    trigger.action === "http"
      ? trigger.httpUrl
      : trigger.action === "check"
        ? (trigger.checks ?? []).join(", ")
        : trigger.action === "uses"
          ? trigger.uses?.ref ?? ""
          : trigger.action === "steps"
            ? `${stepCount} step${stepCount === 1 ? "" : "s"}`
            : trigger.action === "bash"
              ? [trigger.bash?.runtime || "base", trigger.egress || "offline", trigger.bash?.script].filter(Boolean).join(" · ")
              : trigger.worker;
  const manual = (trigger.on ?? []).includes("manual");
  const reusable = (trigger.on ?? []).includes("call");
  const runs = runsQuery.data ?? [];

  function handleRunClick() {
    if (needsRunModal(trigger)) {
      run.reset();
      setModalOpen(true);
      return;
    }
    run.mutate(undefined);
  }

  return (
    <section className="min-w-0 rounded-md border border-slate-200 dark:border-zinc-800">
      <div className="flex items-start justify-between gap-2 px-3 py-2.5">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <p className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">{trigger.id}</p>
            {reusable ? (
              <span className="rounded border border-indigo-200 bg-indigo-50 px-1.5 py-px text-[11px] font-semibold text-indigo-800 dark:border-indigo-900/60 dark:bg-indigo-950/40 dark:text-indigo-200">
                Reusable
              </span>
            ) : null}
            <InheritanceBadge currentFolder={folder} sourceFolder={trigger.folder ?? ""} />
          </div>
          {trigger.description ? (
            <p className="mt-0.5 text-xs text-slate-600 dark:text-zinc-300">{trigger.description}</p>
          ) : null}
          <p className="mt-0.5 text-xs text-slate-600 dark:text-zinc-400">
            on {(trigger.on ?? []).join(", ")} · {trigger.action}{" "}
            <span className="break-all font-mono">{what}</span>
          </p>
          {(trigger.paths ?? []).length > 0 ? (
            <p className="mt-0.5 break-all text-xs text-slate-500 dark:text-zinc-400">
              paths {(trigger.paths ?? []).join(" ")}
            </p>
          ) : null}
          {trigger.action !== "check" && (trigger.checks ?? []).length > 0 ? (
            <p className="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
              reports {(trigger.checks ?? []).join(", ")}
            </p>
          ) : null}
        </div>
        {manual ? (
          <button
            className="shrink-0 rounded-md border border-slate-300 px-2 py-1 text-xs font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-60 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-950"
            disabled={run.isPending}
            onClick={handleRunClick}
            type="button"
          >
            {run.isPending ? "Starting…" : "Run"}
          </button>
        ) : null}
      </div>
      {!modalOpen && run.error ? <p className="px-3 pb-2 text-xs text-rose-700 dark:text-rose-300">{run.error.message}</p> : null}
      <div className="border-t border-slate-100 dark:border-zinc-800">
        {runsQuery.isPending ? null : runs.length === 0 ? (
          <p className="px-3 py-2 text-xs text-slate-500 dark:text-zinc-400">No runs yet.</p>
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-zinc-800">
            {runs.map((r) => (
              <TriggerRunRow
                key={r.id}
                onToggle={() => setOpenRun(openRun === r.id ? "" : (r.id ?? ""))}
                open={openRun === r.id}
                run={r}
              />
            ))}
          </ul>
        )}
      </div>
      <RunAutomationModal
        automation={trigger}
        busy={run.isPending}
        error={run.error?.message}
        onClose={() => setModalOpen(false)}
        onSubmit={(inputs) => run.mutate(inputs)}
        open={modalOpen}
      />
    </section>
  );
}

function TriggerRunRow({ open, onToggle, run }: { open: boolean; onToggle(): void; run: AutomationRun }) {
  const handle = shortChangesetId(run.changesetId ?? "");
  const status = (run.status ?? "").toLowerCase();
  return (
    <li>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2 text-xs">
        <button aria-expanded={open} className="font-semibold text-zinc-900 dark:text-zinc-100" onClick={onToggle} type="button">
          <span
            className={cn(
              "mr-1.5 inline-block h-1.5 w-1.5 rounded-full",
              status === "succeeded"
                ? "bg-emerald-500"
                : status === "failed" || status === "errored" || status === "timed_out"
                  ? "bg-rose-500"
                  : isFinalTriggerStatus(status)
                    ? "bg-slate-400"
                    : "animate-pulse bg-amber-500"
            )}
          />
          {status.replace("_", " ")}
        </button>
        <span className="text-slate-500 dark:text-zinc-400">{run.eventKind}</span>
        {run.templateId ? (
          <span className="rounded border border-indigo-200 bg-indigo-50 px-1.5 py-px font-mono text-[11px] text-indigo-800 dark:border-indigo-900/60 dark:bg-indigo-950/40 dark:text-indigo-200">
            uses {run.templateId}
          </span>
        ) : null}
        {run.triggeredBy ? (
          <span className="text-slate-500 dark:text-zinc-400">
            by <span className="font-mono">{run.triggeredBy}</span>
          </span>
        ) : null}
        {handle ? (
          <Link className="font-mono text-sky-700 underline-offset-2 hover:underline dark:text-sky-300" params={{ id: handle }} to="/cs/$id">
            {handle}
          </Link>
        ) : null}
        <span className="text-slate-500 dark:text-zinc-400">
          <Timestamp relative value={run.createdAt} />
        </span>
        {run.error ? <span className="w-full text-rose-700 dark:text-rose-300">{run.error}</span> : null}
      </div>
      {open ? <AutomationRunLog run={run} /> : null}
    </li>
  );
}
