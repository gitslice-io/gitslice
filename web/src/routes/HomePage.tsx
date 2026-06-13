import { Link, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import { useApi } from "../api/useApi";
import {
  SliceNotice,
  SlicePageHeader,
  SlicePanel,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { useSelection } from "../state/selection";

export function HomePage() {
  const api = useApi();
  const navigate = useNavigate();
  const { account, setAccount } = useSelection();
  const [changesetId, setChangesetId] = useState("");

  const authStatusQuery = useQuery({
    queryKey: ["authStatus"],
    queryFn: () => api.getAuthStatus({})
  });

  const trimmedChangesetId = changesetId.trim();
  const canBrowseSource = account.trim().length > 0;

  function openChangeset(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    if (!trimmedChangesetId) {
      return;
    }

    void navigate({
      params: { id: trimmedChangesetId },
      to: "/changesets/$id"
    });
  }

  return (
    <section className="mx-auto w-full max-w-7xl">
      <SlicePageHeader
        title="Home"
        description="Open one of the web surfaces backed by the current Gitslice APIs."
      />

      <div className="mt-8 grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <SlicePanel className="space-y-6">
          <div>
            <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
              Signed in subject
            </p>
            <div className="mt-2 min-h-8 text-sm font-medium text-zinc-950">
              {authStatusQuery.isLoading ? (
                <span className="inline-block h-4 w-48 animate-pulse rounded bg-slate-200" />
              ) : authStatusQuery.isError ? (
                <span className="text-rose-700">
                  {getErrorMessage(authStatusQuery.error)}
                </span>
              ) : (
                authStatusQuery.data?.subjectId || "No subject returned"
              )}
            </div>
          </div>

          <label className="grid gap-2 text-sm font-medium text-zinc-950">
            Account
            <input
              className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition focus:border-slate-500"
              onChange={(event) => setAccount(event.target.value)}
              placeholder="acme"
              spellCheck={false}
              value={account}
            />
          </label>

          <div className="flex flex-wrap gap-3">
            {canBrowseSource ? (
              <Link
                className="rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
                params={{ account: account.trim() }}
                to="/source/$account"
              >
                Browse Source
              </Link>
            ) : (
              <button
                className="cursor-not-allowed rounded-md bg-slate-200 px-4 py-2 text-sm font-semibold text-slate-500"
                disabled
                type="button"
              >
                Browse Source
              </button>
            )}
            <Link
              className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
              to="/slices"
            >
              List Slices
            </Link>
            <Link
              className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
              to="/changesets/new"
            >
              New Changeset
            </Link>
          </div>
        </SlicePanel>

        <SlicePanel>
          <form className="space-y-4" onSubmit={openChangeset}>
            <div>
              <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
                Open changeset by id
              </p>
              <p className="mt-2 text-sm leading-6 text-slate-600">
                Enter a changeset id or handle that the server can resolve.
              </p>
            </div>
            <label className="grid gap-2 text-sm font-medium text-zinc-950">
              Changeset
              <input
                className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition focus:border-slate-500"
                onChange={(event) => setChangesetId(event.target.value)}
                placeholder="changeset id"
                spellCheck={false}
                value={changesetId}
              />
            </label>
            <button
              className="w-full rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-200 disabled:text-slate-500"
              disabled={!trimmedChangesetId}
              type="submit"
            >
              Open
            </button>
          </form>
        </SlicePanel>
      </div>

      {!account.trim() ? (
        <div className="mt-6">
          <SliceNotice title="Account required">
            Set an account before browsing source or listing slices for a real
            account.
          </SliceNotice>
        </div>
      ) : null}
    </section>
  );
}
