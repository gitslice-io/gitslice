import {
  useMutation,
  useQuery,
  useQueryClient
} from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import type { Changeset, SliceRef } from "../api/types";
import { type ApiClient, useApi } from "../api/useApi";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { cn } from "../lib/cn";

interface ChangesetsSearch {
  slice?: unknown;
}

type ChangesetsQueryKey = readonly ["changesets", string, string];

export function ChangesetsPage() {
  const api = useApi();
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

  return (
    <section className="mx-auto w-full max-w-7xl">
      <SlicePageHeader
        eyebrow="Changesets"
        title={sliceRef ? `${account}/${slice} · Changesets` : "Changesets"}
        description={
          sliceRef
            ? "Review, approve, and merge changesets authored against this slice."
            : "Open a slice and use its Changesets tab to see the slice-scoped review queue."
        }
      />

      <div className="mt-8">
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
  changesets,
  queryKey
}: {
  api: ApiClient;
  changesets: Changeset[];
  queryKey: ChangesetsQueryKey;
}) {
  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="px-4 py-3">Changeset</th>
              <th className="px-4 py-3">Status</th>
              <th className="px-4 py-3">Author</th>
              <th className="px-4 py-3">Approvals</th>
              <th className="px-4 py-3">Blocked reason</th>
              <th className="px-4 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200">
            {changesets.map((changeset) => (
              <ChangesetRow
                api={api}
                changeset={changeset}
                key={changeset.id || changeset.handle || changeset.number}
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
  changeset,
  queryKey
}: {
  api: ApiClient;
  changeset: Changeset;
  queryKey: ChangesetsQueryKey;
}) {
  const queryClient = useQueryClient();
  const changesetId = changeset.id ?? "";
  const detailId = changeset.handle || changesetId;
  const label = changesetLabel(changeset);
  const terminal = isTerminalStatus(changeset.status);
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
      <td className="min-w-72 px-4 py-4">
        <div className="flex flex-col gap-1">
          {detailId ? (
            <Link
              className="group w-fit"
              params={{ id: detailId }}
              to="/changesets/$id"
            >
              <span className="block font-semibold text-zinc-950 underline decoration-slate-300 underline-offset-4 group-hover:decoration-slate-700">
                {label}
              </span>
              <span className="mt-1 block max-w-xl text-sm text-slate-700 group-hover:text-zinc-950">
                {changeset.title || "Untitled changeset"}
              </span>
            </Link>
          ) : (
            <>
              <span className="font-semibold text-zinc-950">{label}</span>
              <span className="max-w-xl text-sm text-slate-700">
                {changeset.title || "Untitled changeset"}
              </span>
            </>
          )}
          {changeset.affectedPaths?.length ? (
            <span className="max-w-xl truncate font-mono text-xs text-slate-500">
              {changeset.affectedPaths.join(", ")}
            </span>
          ) : null}
        </div>
      </td>
      <td className="px-4 py-4">
        <StatusBadge status={changeset.status} />
      </td>
      <td className="px-4 py-4 text-slate-700">
        {changeset.author || "not returned"}
      </td>
      <td className="px-4 py-4 text-slate-700">
        {changeset.submitRequirements?.requiredApprovals ?? "not returned"}
      </td>
      <td className="max-w-sm px-4 py-4 text-slate-700">
        {changeset.submitBlockedReason ? (
          <span>{changeset.submitBlockedReason}</span>
        ) : (
          <span className="text-slate-400">None</span>
        )}
        {rowError ? (
          <p className="mt-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
            {rowError}
          </p>
        ) : null}
      </td>
      <td className="px-4 py-4">
        <div className="flex justify-end gap-2">
          <button
            className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
            disabled={busy || !changesetId || terminal}
            onClick={() => mergeMutation.mutate()}
            type="button"
          >
            {mergeMutation.isPending ? "Merging..." : "Merge"}
          </button>
        </div>
      </td>
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
      setError("Enter a changeset id or handle.");
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
          placeholder="acme/payment@42"
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
      to: "/changesets/$id"
    });
  };
}

function parseSliceSearch(value: unknown): Required<SliceRef> | null {
  if (typeof value !== "string") {
    return null;
  }

  const trimmed = value.trim();
  const slashIndex = trimmed.indexOf("/");

  if (slashIndex <= 0) {
    return null;
  }

  const account = trimmed.slice(0, slashIndex).trim();
  const slice = trimmed.slice(slashIndex + 1).trim();

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

    return (right.handle || right.id || "").localeCompare(
      left.handle || left.id || ""
    );
  });
}

function changesetNumberValue(changeset: Changeset) {
  const value = Number(changeset.number);
  return Number.isFinite(value) ? value : 0;
}

function changesetLabel(changeset: Changeset) {
  if (changeset.handle) {
    return changeset.handle;
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

function isTerminalStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "merged" || normalized === "abandoned";
}
