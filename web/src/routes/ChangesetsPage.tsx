import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

export function ChangesetsPage() {
  const navigate = useNavigate();
  const [changeset, setChangeset] = useState("");
  const [error, setError] = useState("");

  const openChangeset = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const id = changeset.trim();

    if (!id) {
      setError("Enter a changeset id or handle.");
      return;
    }

    setError("");
    void navigate({
      to: "/changesets/$id",
      params: { id }
    });
  };

  return (
    <section className="mx-auto w-full max-w-7xl">
      <div className="border-b border-slate-200 pb-5">
        <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
          Changesets
        </p>
        <h1 className="mt-2 text-2xl font-semibold tracking-normal text-zinc-950">
          Open Changeset
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-slate-600">
          Open a known changeset by canonical id or handle. Changeset listing is
          not available in the current API.
        </p>
      </div>

      <div className="mt-8 grid gap-6 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <form
          className="rounded-lg border border-slate-200 bg-white p-5"
          onSubmit={openChangeset}
        >
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            Changeset
            <input
              className="h-11 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
              onChange={(event) => setChangeset(event.target.value)}
              placeholder="acme/payment@42"
              value={changeset}
            />
          </label>

          {error ? <p className="mt-2 text-sm text-red-700">{error}</p> : null}

          <div className="mt-5 flex flex-wrap gap-3">
            <button
              className="rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px"
              type="submit"
            >
              Open
            </button>
            <Link
              className="rounded-md border border-slate-300 bg-white px-4 py-2.5 text-sm font-medium text-zinc-800 transition hover:border-zinc-500 active:translate-y-px"
              to="/changesets/new"
            >
              Create Changeset
            </Link>
          </div>
        </form>

        <aside className="rounded-lg border border-dashed border-slate-300 bg-white p-5">
          <h2 className="text-sm font-semibold text-zinc-950">Lookup only</h2>
          <p className="mt-2 text-sm leading-6 text-slate-600">
            Use the id returned by the CLI, API, or create flow. Filters,
            pagination, recent changesets, and author search are intentionally
            out of scope for this page.
          </p>
        </aside>
      </div>
    </section>
  );
}
