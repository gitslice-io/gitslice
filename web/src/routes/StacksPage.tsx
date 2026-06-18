import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import type { ChangesetStack, SliceRef } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader
} from "../components/slices/SlicePageParts";
import { cn } from "../lib/cn";
import {
  formatCommit,
  formatTimestamp,
  getErrorMessage,
  parseSliceSearch,
  secondaryButtonClass,
  shortStackId,
  sliceRefLabel,
  stackDisplayName,
  StackStatusBadge
} from "./stackPageUtils";

interface StacksSearch {
  slice?: unknown;
  status?: unknown;
}

export function StacksPage() {
  const api = useApi();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as StacksSearch;
  const sliceRef = parseSliceSearch(search.slice);
  const status = typeof search.status === "string" ? search.status : "";
  const sliceLabel = sliceRefLabel(sliceRef);
  const queryKey = useMemo(
    () => ["stacks", sliceRef?.account ?? "", sliceRef?.slice ?? "", status],
    [sliceRef?.account, sliceRef?.slice, status]
  );

  const stacksQuery = useQuery({
    enabled: Boolean(sliceRef),
    queryKey,
    queryFn: () =>
      api.listStacks({
        authoringSlice: sliceRef ?? undefined,
        status,
        limit: 100
      })
  });

  const stacks = useMemo(
    () => sortStacks(stacksQuery.data?.stacks ?? []),
    [stacksQuery.data?.stacks]
  );

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="mb-4">
        <Breadcrumb
          items={[
            { label: "Slices", to: "/slices" },
            sliceLabel ? { label: sliceLabel } : { label: "Stacks" }
          ]}
        />
      </div>
      <SlicePageHeader
        actions={
          <Link
            className={secondaryButtonClass}
            search={sliceLabel ? ({ slice: sliceLabel } as never) : undefined}
            to="/stacks/new"
          >
            Create stack
          </Link>
        }
        description={
          sliceLabel
            ? "Review stack trees, open individual entries, and start stack-level restack or submit flows."
            : "Open a stack by id, or choose a slice scope to list active stack review trees."
        }
        eyebrow="Stacks"
        title={sliceLabel ? `${sliceLabel} · Stacks` : "Stacks"}
      />

      <div className="mt-8 grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="min-w-0">
          {!sliceRef ? (
            <SliceNotice title="Choose a slice to list stacks">
              Stack lists are slice-scoped. Use the lookup panel to open a known
              stack directly, or enter an account and slice to list stacks.
            </SliceNotice>
          ) : stacksQuery.isLoading ? (
            <SliceLoadingBlock />
          ) : stacksQuery.isError ? (
            <SliceNotice title="Could not load stacks" tone="error">
              {getErrorMessage(stacksQuery.error)}
            </SliceNotice>
          ) : stacks.length === 0 ? (
            <SliceNotice title="No stacks for this slice yet">
              New stack review trees created against this slice will appear here.
            </SliceNotice>
          ) : (
            <StacksTable stacks={stacks} />
          )}
        </div>

        <div className="grid content-start gap-4">
          <StackScopeForm
            initialSlice={sliceLabel}
            initialStatus={status}
            onScope={(nextSlice, nextStatus) => {
              void navigate({
                search: {
                  slice: nextSlice,
                  ...(nextStatus ? { status: nextStatus } : {})
                } as never,
                to: "/stacks"
              });
            }}
          />
          <OpenStackForm
            onOpen={(id) => {
              void navigate({
                params: { id },
                to: "/stacks/$id"
              });
            }}
          />
        </div>
      </div>
    </section>
  );
}

function StacksTable({ stacks }: { stacks: ChangesetStack[] }) {
  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="px-3 py-3 sm:px-4">Stack</th>
              <th className="hidden px-4 py-3 sm:table-cell">Status</th>
              <th className="hidden px-4 py-3 md:table-cell">Entries</th>
              <th className="hidden px-4 py-3 lg:table-cell">Base</th>
              <th className="hidden px-4 py-3 lg:table-cell">Updated</th>
              <th className="px-3 py-3 text-right sm:px-4">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200">
            {stacks.map((stack) => (
              <tr className="align-top transition hover:bg-slate-50" key={stack.id}>
                <td className="max-w-[15rem] px-3 py-4 sm:max-w-none sm:px-4">
                  <Link
                    className="group min-w-0"
                    params={{ id: stack.id || "" }}
                    to="/stacks/$id"
                  >
                    <span className="block break-words font-semibold text-zinc-950 underline decoration-slate-300 underline-offset-4 group-hover:decoration-slate-700">
                      {stackDisplayName(stack)}
                    </span>
                    <span className="mt-1 block break-all font-mono text-xs text-slate-500">
                      {shortStackId(stack.id) || stack.id}
                    </span>
                  </Link>
                </td>
                <td className="hidden px-4 py-4 sm:table-cell">
                  <StackStatusBadge status={stack.status} />
                </td>
                <td className="hidden px-4 py-4 text-slate-700 md:table-cell">
                  {stack.entries?.length ?? 0}
                </td>
                <td
                  className="hidden px-4 py-4 font-mono text-xs text-slate-600 lg:table-cell"
                  title={stack.baseCommitId}
                >
                  {formatCommit(stack.baseCommitId)}
                </td>
                <td className="hidden px-4 py-4 text-slate-700 lg:table-cell">
                  {formatTimestamp(stack.updatedAt)}
                </td>
                <td className="px-3 py-4 sm:px-4">
                  <div className="flex flex-wrap justify-end gap-2">
                    <Link
                      className={cn(secondaryButtonClass, "px-3 py-2")}
                      params={{ id: stack.id || "" }}
                      to="/stacks/$id/restack"
                    >
                      Restack
                    </Link>
                    <Link
                      className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px"
                      params={{ id: stack.id || "" }}
                      to="/stacks/$id/submit"
                    >
                      Submit
                    </Link>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function StackScopeForm({
  initialSlice,
  initialStatus,
  onScope
}: {
  initialSlice: string;
  initialStatus: string;
  onScope(slice: string, status: string): void;
}) {
  const [slice, setSlice] = useState(initialSlice);
  const [status, setStatus] = useState(initialStatus);
  const [error, setError] = useState("");

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmed = slice.trim();
    if (!parseSliceSearch(trimmed)) {
      setError("Enter a slice as account:slice.");
      return;
    }
    setError("");
    onScope(trimmed, status.trim());
  };

  return (
    <form
      className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
      onSubmit={submit}
    >
      <label className="grid gap-2 text-sm font-medium text-zinc-800">
        Slice scope
        <input
          className={inputClass}
          onChange={(event) => setSlice(event.target.value)}
          placeholder="acme:payment"
          value={slice}
        />
      </label>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Status
        <select
          className={inputClass}
          onChange={(event) => setStatus(event.target.value)}
          value={status}
        >
          <option value="">Active stacks</option>
          <option value="open">Open</option>
          <option value="partial">Partial</option>
          <option value="closed">Closed</option>
        </select>
      </label>
      {error ? <p className="mt-2 text-sm text-rose-700">{error}</p> : null}
      <button className="mt-5 w-full rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px" type="submit">
        List stacks
      </button>
    </form>
  );
}

function OpenStackForm({ onOpen }: { onOpen(id: string): void }) {
  const [stackId, setStackId] = useState("");
  const [error, setError] = useState("");

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const id = stackId.trim();
    if (!id) {
      setError("Enter a stack id.");
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
        Open stack
        <input
          className={inputClass}
          onChange={(event) => setStackId(event.target.value)}
          placeholder="stk_..."
          value={stackId}
        />
      </label>
      {error ? <p className="mt-2 text-sm text-rose-700">{error}</p> : null}
      <button className="mt-5 w-full rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px" type="submit">
        Open
      </button>
    </form>
  );
}

function sortStacks(stacks: ChangesetStack[]) {
  return [...stacks].sort((left, right) => {
    const rightTime = Date.parse(right.updatedAt || right.createdAt || "");
    const leftTime = Date.parse(left.updatedAt || left.createdAt || "");
    const safeRight = Number.isFinite(rightTime) ? rightTime : 0;
    const safeLeft = Number.isFinite(leftTime) ? leftTime : 0;

    if (safeRight !== safeLeft) {
      return safeRight - safeLeft;
    }

    return (right.id || "").localeCompare(left.id || "");
  });
}

const inputClass =
  "h-11 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200";
