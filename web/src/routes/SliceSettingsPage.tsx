import { useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import type { AgentDaemon, Slice, SliceDefinition } from "../api/types";
import { useApi } from "../api/useApi";
import { shortHash } from "../lib/objectId";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { SliceTabs } from "../components/SliceTabs";
import {
  SliceDefinitionForm,
  toVisibilityOption,
  validateIncludedPaths,
  type VisibilityOption
} from "../components/slices/SliceDefinitionForm";
import {
  SliceLoadingBlock,
  SliceMetadataGrid,
  SliceNotice,
  SlicePageHeader,
  SlicePanel,
  getErrorMessage,
  sliceDisplayName
} from "../components/slices/SlicePageParts";
import { toSliceRouteParams } from "../lib/sliceRoutes";

interface SliceParams {
  account?: string;
  slice?: string;
}

export function SliceSettingsPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const params = useParams({ strict: false }) as SliceParams;
  const routeAccount = params.account ?? "";
  const routeSlice = params.slice ?? "";
  const routeSliceRef =
    routeAccount && routeSlice
      ? { account: routeAccount, slice: routeSlice }
      : undefined;
  const [visibility, setVisibility] = useState<VisibilityOption>("private");
  const [includedPaths, setIncludedPaths] = useState<string[]>([]);
  const [clientErrors, setClientErrors] = useState<string[]>([]);
  const [saveMessage, setSaveMessage] = useState<string | null>(null);
  const [ciSaveMessage, setCiSaveMessage] = useState<string | null>(null);
  const [conflictMessage, setConflictMessage] = useState<string | null>(null);

  const sliceQuery = useQuery({
    enabled: Boolean(routeSliceRef),
    queryKey: ["sliceRef", routeAccount, routeSlice],
    queryFn: () => api.resolveSlice({ ref: routeSliceRef })
  });

  const slice = sliceQuery.data;
  const sliceId = slice?.id ?? "";
  const sliceRouteParams = toSliceRouteParams(slice?.ref ?? routeSliceRef);
  const sliceRouteKey = sliceRouteParams
    ? `${sliceRouteParams.account}:${sliceRouteParams.slice}`
    : `${routeAccount}:${routeSlice}`;

  const daemonsQuery = useQuery({
    enabled: Boolean(slice),
    queryKey: ["agentDaemons"],
    queryFn: async () => (await api.listDaemons({})).daemons ?? []
  });

  useEffect(() => {
    if (!slice) {
      return;
    }

    setVisibility(toVisibilityOption(slice.definition?.visibility));
    setIncludedPaths(slice.definition?.includedPaths ?? []);
    setClientErrors([]);
  }, [slice?.definitionHash, slice?.id]);

  const updateMutation = useMutation({
    mutationFn: async (normalizedPaths: string[]) => {
      if (!slice) {
        throw new Error("Slice has not loaded yet.");
      }
      if (!slice.id) {
        throw new Error("Slice has no internal id.");
      }

      const definition: SliceDefinition = {
        ...(slice.definition ?? {}),
        includedPaths: normalizedPaths,
        sliceId: slice.definition?.sliceId || slice.id,
        visibility
      };

      return api.updateSliceDefinition({
        definition,
        expectedDefinitionHash: slice.definitionHash,
        sliceId: slice.id
      });
    },
    onError: (error) => {
      setSaveMessage(null);

      if (isHashConflict(error)) {
        setConflictMessage(
          "The slice definition changed on the server. The latest definition has been reloaded; review it and save again."
        );
        void queryClient.invalidateQueries({
          queryKey: ["sliceRef", routeAccount, routeSlice]
        });
      }
    },
    onSuccess: async () => {
      setConflictMessage(null);
      setSaveMessage("Definition saved.");
      await queryClient.invalidateQueries({
        queryKey: ["sliceRef", routeAccount, routeSlice]
      });
      await queryClient.invalidateQueries({ queryKey: ["slices"] });
    }
  });

  const ciDaemonMutation = useMutation({
    mutationFn: async (daemonId: string) => {
      const targetSlice = slice?.ref ?? routeSliceRef;
      if (!targetSlice?.account || !targetSlice.slice) {
        throw new Error("Slice reference is not available.");
      }

      return api.setSliceCIDaemon({
        daemonId,
        slice: targetSlice
      });
    },
    onError: () => {
      setCiSaveMessage(null);
    },
    onMutate: () => {
      setCiSaveMessage(null);
    },
    onSuccess: async (updatedSlice) => {
      setCiSaveMessage("CI daemon updated.");
      queryClient.setQueryData(
        ["sliceRef", routeAccount, routeSlice],
        updatedSlice
      );
      await queryClient.invalidateQueries({
        queryKey: ["sliceRef", routeAccount, routeSlice]
      });
      await queryClient.invalidateQueries({ queryKey: ["slices"] });
    }
  });

  function saveDefinition(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const validation = validateIncludedPaths(includedPaths);
    setClientErrors(validation.errors);
    setSaveMessage(null);
    setConflictMessage(null);

    if (validation.errors.length > 0) {
      return;
    }

    updateMutation.mutate(validation.paths);
  }

  if (sliceQuery.isLoading) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SliceLoadingBlock />
      </section>
    );
  }

  if (sliceQuery.isError) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SlicePageHeader title="Slice Settings" />
        <div className="mt-8">
          <SliceNotice title="Could not load slice" tone="error">
            {getErrorMessage(sliceQuery.error)}
          </SliceNotice>
        </div>
      </section>
    );
  }

  if (!slice) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SlicePageHeader title="Slice Settings" />
        <div className="mt-8">
          <SliceNotice title="Slice not found">
            No slice was returned for {sliceRouteKey || "unknown"}.
          </SliceNotice>
        </div>
      </section>
    );
  }

  const sliceLabel = sliceDisplayName(slice);

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        breadcrumb={
          <Breadcrumb
            items={[
              { label: "Home", to: "/" },
              sliceRouteParams
                ? {
                    label: sliceLabel,
                    params: sliceRouteParams,
                    to: "/slices/$account/$slice"
                  }
                : { label: sliceLabel },
              { label: "Settings" }
            ]}
          />
        }
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 sm:text-lg">
            Settings
          </h1>
        }
        tabs={
          sliceRouteParams ? (
            <SliceTabs
              active="settings"
              params={sliceRouteParams}
              sliceLabel={sliceLabel}
            />
          ) : undefined
        }
      />
      <p className="mb-4 text-sm leading-6 text-slate-600">
        Edit the supported slice definition fields only.
      </p>

      <form className="mt-8 space-y-6" onSubmit={saveDefinition}>
        <SlicePanel>
          <SliceMetadataGrid
            rows={[
              {
                label: "Current hash",
                value: (
                  <code className="font-mono text-xs" title={slice.definitionHash}>
                    {slice.definitionHash ? shortHash(slice.definitionHash) : "none"}
                  </code>
                )
              },
              {
                label: "Version",
                value: slice.definition?.version ?? "unknown"
              },
              {
                label: "Slice id",
                value: (
                  <code className="font-mono text-xs">{slice.id || sliceId}</code>
                )
              },
              {
                label: "Loaded visibility",
                value: slice.definition?.visibility || "unspecified"
              }
            ]}
          />
        </SlicePanel>

        <SliceCIDaemonPanel
          currentDaemonId={slice.ciDaemonId ?? ""}
          daemons={daemonsQuery.data ?? []}
          error={
            daemonsQuery.error
              ? getErrorMessage(daemonsQuery.error)
              : ciDaemonMutation.error
                ? getErrorMessage(ciDaemonMutation.error)
                : ""
          }
          isLoading={daemonsQuery.isPending}
          message={ciSaveMessage}
          onChange={(daemonId) => ciDaemonMutation.mutate(daemonId)}
          pending={ciDaemonMutation.isPending}
          slice={slice}
        />

        <SliceDefinitionForm
          account={slice?.ref?.account}
          disabled={updateMutation.isPending}
          includedPaths={includedPaths}
          includedPathsLocked={slice?.ref?.slice === "home"}
          onIncludedPathsChange={setIncludedPaths}
          onVisibilityChange={setVisibility}
          visibility={visibility}
        />

        {clientErrors.length > 0 ? (
          <SliceNotice title="Fix included paths" tone="error">
            <ul className="list-disc space-y-1 pl-5">
              {clientErrors.map((error) => (
                <li className="break-all" key={error}>{error}</li>
              ))}
            </ul>
          </SliceNotice>
        ) : null}

        {updateMutation.isError && !conflictMessage ? (
          <SliceNotice title="Could not save definition" tone="error">
            {getErrorMessage(updateMutation.error)}
          </SliceNotice>
        ) : null}

        {conflictMessage ? (
          <SliceNotice title="Definition hash conflict" tone="error">
            {conflictMessage}
          </SliceNotice>
        ) : null}

        {saveMessage ? (
          <SliceNotice title={saveMessage} tone="success">
            The latest slice definition is being refreshed from the server.
          </SliceNotice>
        ) : null}

        <div className="flex flex-wrap items-center gap-3">
          <button
            className="rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-200 disabled:text-slate-500"
            disabled={updateMutation.isPending}
            type="submit"
          >
            {updateMutation.isPending ? "Saving..." : "Save definition"}
          </button>
          <button
            className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
            disabled={sliceQuery.isFetching}
            onClick={() => {
              void sliceQuery.refetch();
            }}
            type="button"
          >
            Reload
          </button>
        </div>
      </form>
    </section>
  );
}

function SliceCIDaemonPanel({
  currentDaemonId,
  daemons,
  error,
  isLoading,
  message,
  onChange,
  pending,
  slice
}: {
  currentDaemonId: string;
  daemons: AgentDaemon[];
  error: string;
  isLoading: boolean;
  message: string | null;
  onChange(daemonId: string): void;
  pending: boolean;
  slice: Slice;
}) {
  const onlineDaemons = daemons.filter((daemon) => daemon.status === "online");
  const currentDaemon = daemons.find((daemon) => daemon.id === currentDaemonId);
  const currentIsOnline = onlineDaemons.some(
    (daemon) => daemon.id === currentDaemonId
  );
  const canUpdate = Boolean(slice.ref?.account && slice.ref.slice);

  return (
    <SlicePanel>
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(16rem,24rem)] lg:items-start">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-zinc-950">CI daemon</h2>
          <p className="mt-1 text-sm leading-6 text-slate-600">
            Full-tree checks for this slice run on the selected online agent daemon.
          </p>
          <p className="mt-3 text-xs font-semibold uppercase tracking-normal text-slate-500">
            Current
          </p>
          <code className="mt-1 block break-all font-mono text-xs text-zinc-950">
            {currentDaemonId || "none"}
          </code>
        </div>

        <div className="grid gap-2">
          <label className="grid gap-1.5 text-sm font-medium text-zinc-800">
            CI daemon
            <select
              aria-label="CI daemon"
              className="min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-500"
              disabled={isLoading || pending || !canUpdate}
              onChange={(event) => onChange(event.target.value)}
              value={currentDaemonId}
            >
              <option value="">None</option>
              {currentDaemonId && !currentIsOnline ? (
                <option value={currentDaemonId}>
                  {daemonLabel(currentDaemon, currentDaemonId)} (current)
                </option>
              ) : null}
              {onlineDaemons.map((daemon) => (
                <option key={daemon.id ?? daemon.name} value={daemon.id ?? ""}>
                  {daemonLabel(daemon, daemon.id ?? "")}
                </option>
              ))}
            </select>
          </label>
          {isLoading ? (
            <p className="text-xs text-slate-500">Loading daemons...</p>
          ) : onlineDaemons.length === 0 ? (
            <p className="text-xs leading-5 text-slate-500">
              No online daemons are available. You can still clear the current
              runner by choosing None.
            </p>
          ) : null}
          {error ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-900">
              {error}
            </p>
          ) : message ? (
            <p className="rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-900">
              {message}
            </p>
          ) : null}
        </div>
      </div>
    </SlicePanel>
  );
}

function daemonLabel(daemon: AgentDaemon | undefined, fallback: string) {
  if (!daemon) {
    return fallback;
  }
  const name = daemon.name || daemon.id || fallback;
  const runtime = [daemon.runtime, daemon.version].filter(Boolean).join(" ");
  return runtime ? `${name} - ${runtime}` : name;
}

function isHashConflict(error: unknown) {
  const maybeError = error as {
    code?: string | number;
    message?: string;
    status?: number;
  };
  const text = `${maybeError.code ?? ""} ${
    maybeError.message ?? ""
  }`.toLowerCase();

  return (
    maybeError.status === 409 ||
    text.includes("conflict") ||
    text.includes("definition hash") ||
    text.includes("expected_definition_hash") ||
    text.includes("stale")
  );
}
