import { useAuth } from "@clerk/clerk-react";
import {
  useMutation,
  useQuery,
  useQueryClient
} from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import type { Changeset, SliceRef } from "../api/types";
import { type ApiClient, useApi } from "../api/useApi";
import { Breadcrumb, type Crumb } from "../components/Breadcrumb";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { cn } from "../lib/cn";
import { shortChangesetId } from "../lib/objectId";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { displaySubmitBlockedReason } from "./stackPageUtils";

interface ChangesetsSearch {
  slice?: unknown;
}

type ChangesetsQueryKey = readonly ["changesets", string, string];

export function ChangesetsPage() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const canManage = Boolean(isLoaded && isSignedIn);
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as ChangesetsSearch;
  const sliceRef = parseSliceSearch(search.slice);
  const account = sliceRef?.account ?? "";
  const slice = sliceRef?.slice ?? "";
  const queryKey = useMemo<ChangesetsQueryKey>(
    () => ["changesets", account, slice],
    [account, slice]
  );

  const changesetsQuery = useQuery({
    enabled: Boolean(account && slice),
    queryKey,
    queryFn: () =>
      api.listChangesets({
        authoringSlice: { account, slice }
      })
  });

  const changesets = useMemo(
    () => sortChangesets(changesetsQuery.data?.changesets ?? []),
    [changesetsQuery.data?.changesets]
  );

  const breadcrumbItems: Crumb[] = [{ label: "Slices", to: "/slices" }];
  if (sliceRef) {
    const routeParams = toSliceRouteParams(sliceRef);
    breadcrumbItems.push(
      routeParams
        ? {
            label: `${account}:${slice}`,
            to: "/slices/$account/$slice",
            params: routeParams
          }
        : { label: `${account}:${slice}` }
    );
  }
  breadcrumbItems.push({ label: "Changesets" });

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="mb-4">
        <Breadcrumb items={breadcrumbItems} />
      </div>
      <SlicePageHeader
        eyebrow="Changesets"
        title={sliceRef ? `${account}:${slice} · Changesets` : "Changesets"}
        description={
          sliceRef
            ? "Review and merge changesets authored against this slice."
            : "Open a slice and use its Changesets tab to see the slice-scoped review queue."
        }
      />
      <div className="mt-6">
        {!sliceRef ? (
          <MissingSliceState navigateToChangeset={navigateToChangeset(navigate)} />
        ) : changesetsQuery.isLoading ? (
          <SliceLoadingBlock />
        ) : changesetsQuery.isError ? (
          <SliceNotice title="Could not load changesets" tone="error">
            {getErrorMessage(changesetsQuery.error)}
          </SliceNotice>
        ) : changesets.length === 0 ? (
          <SliceNotice title="No changesets for this slice yet.">
            New changesets created from the slice workspace will appear here.
          </SliceNotice>
        ) : (
          <ChangesetsTable
            api={api}
            canManage={canManage}
            changesets={changesets}
            queryKey={queryKey}
          />
        )}
      </div>
    </section>
  );
}

function ChangesetsTable({
  api,
  canManage,
  changesets,
  queryKey
}: {
  api: ApiClient;
  canManage: boolean;
  changesets: Changeset[];
  queryKey: ChangesetsQueryKey;
}) {
  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="px-3 py-3 sm:px-4">Changeset</th>
              <th className="hidden px-3 py-3 sm:table-cell sm:px-4">Status</th>
              <th className="hidden px-4 py-3 md:table-cell">Author</th>
              <th className="hidden px-4 py-3 md:table-cell">Approvals</th>
              <th className="hidden px-4 py-3 md:table-cell">
                Blocked reason
              </th>
              {canManage ? (
                <th className="px-3 py-3 text-right sm:px-4">Actions</th>
              ) : null}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200">
            {changesets.map((changeset) => (
              <ChangesetRow
                api={api}
                canManage={canManage}
                changeset={changeset}
                key={changeset.id || changeset.number}
                queryKey={queryKey}
              />
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function ChangesetRow({
  api,
  canManage,
  changeset,
  queryKey
}: {
  api: ApiClient;
  canManage: boolean;
  changeset: Changeset;
  queryKey: ChangesetsQueryKey;
}) {
  const queryClient = useQueryClient();
  const changesetId = changeset.id ?? "";
  const detailId = shortChangesetId(changesetId) || changesetId;
  const label = changesetLabel(changeset);
  const mergeable = isMergeableStatus(changeset.status);
  const [rowError, setRowError] = useState("");

  const invalidateList = async () => {
    await queryClient.invalidateQueries({ queryKey });
  };

  const mergeMutation = useMutation({
    mutationFn: async () => {
      if (!changesetId) {
        throw new Error("This changeset did not return an id.");
      }

      return api.submitChangeset({
        changesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId
      });
    },
    onError: (error) => setRowError(getErrorMessage(error)),
    onMutate: () => setRowError(""),
    onSuccess: async () => {
      setRowError("");
      await invalidateList();
    }
  });

  const busy = mergeMutation.isPending;

  return (
    <tr className="align-top transition hover:bg-slate-50">
      <td className="max-w-[14rem] px-3 py-4 sm:min-w-72 sm:max-w-none sm:px-4">
        <div className="flex flex-col gap-1">
          <div className="sm:hidden">
            <StatusBadge status={changeset.status} />
          </div>
          {detailId ? (
            <Link
              className="group min-w-0"
              params={{ id: detailId }}
              to="/cs/$id"
            >
              <span className="block break-words font-semibold text-zinc-950 underline decoration-slate-300 underline-offset-4 group-hover:decoration-slate-700">
                {label}
              </span>
              <span className="mt-1 block max-w-xl break-words text-sm text-slate-700 group-hover:text-zinc-950">
                {changeset.title || "Untitled changeset"}
              </span>
            </Link>
          ) : (
            <>
              <span className="font-semibold text-zinc-950">{label}</span>
              <span className="max-w-xl break-words text-sm text-slate-700">
                {changeset.title || "Untitled changeset"}
              </span>
            </>
          )}
          {changeset.affectedPaths?.length ? (
            <span
              className="text-xs text-slate-500"
              title={changeset.affectedPaths.join("\n")}
            >
              {changeset.affectedPaths.length}{" "}
              {changeset.affectedPaths.length === 1 ? "file" : "files"} changed
            </span>
          ) : null}
          {changeset.submitBlockedReason ? (
            <p className="mt-2 rounded-md border border-amber-200 bg-amber-50 px-2 py-1 text-xs leading-5 text-amber-900 md:hidden">
              {displaySubmitBlockedReason(changeset.submitBlockedReason)}
            </p>
          ) : null}
          {rowError ? (
            <p className="mt-2 rounded-md border border-red-200 bg-red-50 px-2 py-1 text-xs leading-5 text-red-800 md:hidden">
              {rowError}
            </p>
          ) : null}
        </div>
      </td>
      <td className="hidden px-3 py-4 sm:table-cell sm:px-4">
        <StatusBadge status={changeset.status} />
      </td>
      <td className="hidden px-4 py-4 text-slate-700 md:table-cell">
        {changeset.author || "not returned"}
      </td>
      <td className="hidden px-4 py-4 text-slate-700 md:table-cell">
        {changeset.submitRequirements?.requiredApprovals ?? "not returned"}
      </td>
      <td className="hidden max-w-sm px-4 py-4 text-slate-700 md:table-cell">
        {changeset.submitBlockedReason ? (
          <span>{displaySubmitBlockedReason(changeset.submitBlockedReason)}</span>
        ) : (
          <span className="text-slate-400">None</span>
        )}
        {rowError ? (
          <p className="mt-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
            {rowError}
          </p>
        ) : null}
      </td>
      {canManage ? (
        <td className="px-3 py-4 sm:px-4">
          <div className="flex flex-wrap justify-end gap-2">
            <button
              className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
              disabled={busy || !changesetId || !mergeable}
              onClick={() => mergeMutation.mutate()}
              type="button"
            >
              {mergeMutation.isPending ? "Merging..." : "Merge"}
            </button>
          </div>
        </td>
      ) : null}
    </tr>
  );
}

function MissingSliceState({
  navigateToChangeset
}: {
  navigateToChangeset(id: string): void;
}) {
  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <SliceNotice title="Open a slice first">
        Use a slice page&apos;s Changesets tab to open the slice-scoped list.
      </SliceNotice>
      <OpenChangesetForm onOpen={navigateToChangeset} />
    </div>
  );
}

function OpenChangesetForm({ onOpen }: { onOpen(id: string): void }) {
  const [changeset, setChangeset] = useState("");
  const [error, setError] = useState("");

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const id = changeset.trim();

    if (!id) {
      setError("Enter a changeset id.");
      return;
    }

    setError("");
    onOpen(id);
  };

  return (
    <form
      className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
      onSubmit={submit}
    >
      <label className="grid gap-2 text-sm font-medium text-zinc-800">
        Open changeset
        <input
          className="h-11 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => setChangeset(event.target.value)}
          placeholder="3f9a2b1c4d"
          value={changeset}
        />
      </label>

      {error ? <p className="mt-2 text-sm text-red-700">{error}</p> : null}

      <button
        className="mt-5 rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px"
        type="submit"
      >
        Open
      </button>
    </form>
  );
}

function navigateToChangeset(navigate: ReturnType<typeof useNavigate>) {
  return (id: string) => {
    void navigate({
      params: { id },
      to: "/cs/$id"
    });
  };
}

function parseSliceSearch(value: unknown): Required<SliceRef> | null {
  if (typeof value !== "string") {
    return null;
  }

  const trimmed = value.trim();
  // Canonical handle is account:slice; also accept the legacy account/slice.
  const sep = trimmed.includes(":") ? ":" : "/";
  const index = trimmed.indexOf(sep);

  if (index <= 0) {
    return null;
  }

  const account = trimmed.slice(0, index).trim();
  const slice = trimmed.slice(index + 1).trim();

  if (!account || !slice) {
    return null;
  }

  return { account, slice };
}

function sortChangesets(changesets: Changeset[]) {
  return [...changesets].sort((left, right) => {
    const rightNumber = changesetNumberValue(right);
    const leftNumber = changesetNumberValue(left);

    if (rightNumber !== leftNumber) {
      return rightNumber - leftNumber;
    }

    return (right.id || "").localeCompare(left.id || "");
  });
}

function changesetNumberValue(changeset: Changeset) {
  const value = Number(changeset.number);
  return Number.isFinite(value) ? value : 0;
}

function changesetLabel(changeset: Changeset) {
  const shortId = shortChangesetId(changeset.id || "");
  if (shortId) {
    return shortId;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "Changeset";
}

function StatusBadge({ status }: { status?: string }) {
  return (
    <span
      className={cn(
        "inline-flex rounded-md border px-2 py-1 text-xs font-semibold",
        statusClass(status)
      )}
    >
      {status || "unknown"}
    </span>
  );
}

function statusClass(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "published":
    case "merged":
    case "submitted":
      return "border-emerald-200 bg-emerald-50 text-emerald-800";
    case "pending_publish":
      return "border-amber-200 bg-amber-50 text-amber-900";
    case "abandoned":
      return "border-red-200 bg-red-50 text-red-800";
    case "draft":
      return "border-slate-200 bg-slate-50 text-slate-700";
    default:
      return "border-slate-200 bg-slate-50 text-slate-700";
  }
}

// Submit (merge) is only valid while the changeset is still open. Once it has
// been submitted/published/abandoned it can't be merged again.
function isMergeableStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "" || normalized === "draft" || normalized === "open";
}
