import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { SliceNotice, SlicePanel, getErrorMessage } from "../components/slices/SlicePageParts";
import { useSelection } from "../state/selection";

// The same shape as usernames (internal/usernames); the server has the final
// word, including names reserved for sign-up.
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{2,61}[a-z0-9]$/;

export function normalizeOrganizationName(value: string) {
  return value.trim().toLowerCase().replace(/_/g, "-");
}

export function validateOrganizationName(value: string) {
  const name = normalizeOrganizationName(value);
  if (!name) {
    return "Enter a name.";
  }
  if (!NAME_PATTERN.test(name)) {
    return "Use 4 to 63 letters, digits and hyphens, starting and ending with a letter or digit.";
  }
  return "";
}

export function NewOrganizationPage() {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { setActiveAccount } = useSelection();
  const [name, setName] = useState("");
  const [error, setError] = useState("");

  const create = useMutation({
    mutationFn: (slug: string) => api.createOrganization({ slug }),
    onError: (err) => setError(getErrorMessage(err)),
    onSuccess: async (created) => {
      const account = created.account ?? normalizeOrganizationName(name);
      await queryClient.invalidateQueries({ queryKey: ["authStatus"] });
      setActiveAccount(account);
      void navigate({ params: { account }, to: "/accounts/$account" });
    }
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const problem = validateOrganizationName(name);
    setError(problem);
    if (!problem) {
      create.mutate(normalizeOrganizationName(name));
    }
  }

  return (
    <section className="mx-auto w-full max-w-3xl">
      <PageHeader
        breadcrumb={<Breadcrumb items={[{ label: "Home", to: "/" }, { label: "New organization" }]} />}
        title={<h1 className="text-base font-semibold text-zinc-950 dark:text-zinc-50 sm:text-lg">New organization</h1>}
      />
      <p className="mb-6 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        An organization holds slices that belong to a team rather than to one person. You become its owner and invite
        the others; they join when they accept.
      </p>
      <form onSubmit={submit}>
        <SlicePanel className="space-y-4">
          <label className="grid gap-2 text-sm font-medium text-zinc-950 dark:text-zinc-50">
            Organization name
            <input
              aria-invalid={Boolean(error) || undefined}
              autoFocus
              className="h-10 rounded-md border border-slate-300 bg-white px-3 font-mono text-sm text-zinc-950 outline-none focus:border-slate-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50"
              disabled={create.isPending}
              onChange={(event) => {
                setName(event.target.value);
                setError("");
              }}
              placeholder="acme-labs"
              spellCheck={false}
              value={name}
            />
            <span className="text-xs font-normal text-slate-500 dark:text-zinc-400">
              It is the organization's account: its slices live under /{normalizeOrganizationName(name) || "name"}.
            </span>
          </label>
          {error ? (
            <SliceNotice title="Could not create the organization" tone="error">
              {error}
            </SliceNotice>
          ) : null}
          <button
            className="rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 disabled:opacity-60 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
            disabled={create.isPending}
            type="submit"
          >
            {create.isPending ? "Creating..." : "Create organization"}
          </button>
        </SlicePanel>
      </form>
    </section>
  );
}
