import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import {
  SliceDefinitionForm,
  validateIncludedPaths,
  type VisibilityOption
} from "../components/slices/SliceDefinitionForm";
import {
  SliceNotice,
  SlicePanel,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";

export function SliceCreatePage() {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const selection = useSelection();
  const account = selection.account.trim();
  const [sliceName, setSliceName] = useState("");
  const [visibility, setVisibility] = useState<VisibilityOption>("private");
  const [includedPaths, setIncludedPaths] = useState<string[]>([]);
  const [nameError, setNameError] = useState("");
  const [clientErrors, setClientErrors] = useState<string[]>([]);
  const [serverError, setServerError] = useState("");

  const createMutation = useMutation({
    mutationFn: async (normalizedPaths: string[]) => {
      const name = sliceName.trim();

      if (!account) {
        throw new Error("Signed-in account is not available.");
      }

      const created = await api.createSlice({
        ref: { account, slice: name },
        includedPaths: normalizedPaths,
        visibility
      });

      if (!created.id) {
        throw new Error("The server created the slice without returning an id.");
      }

      return created;
    },
    onError: (error) => {
      setServerError(getErrorMessage(error));
    },
    onMutate: () => {
      setServerError("");
    },
    onSuccess: async (created) => {
      const routeParams =
        toSliceRouteParams(created.ref) || {
          account,
          slice: sliceName.trim()
        };

      if (!routeParams.account || !routeParams.slice) {
        return;
      }

      await queryClient.invalidateQueries({ queryKey: ["slices"] });
      void navigate({
        params: routeParams,
        to: "/slices/$account/$slice"
      });
    }
  });

  function createSlice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const nextNameError = validateSliceName(sliceName);
    const validation = validateIncludedPaths(includedPaths);
    const nextClientErrors = [...validation.errors];

    if (!account) {
      nextClientErrors.unshift("Signed-in account is not available.");
    }

    setNameError(nextNameError);
    setClientErrors(nextClientErrors);
    setServerError("");

    if (nextNameError || nextClientErrors.length > 0) {
      return;
    }

    createMutation.mutate(validation.paths);
  }

  const isBusy = createMutation.isPending;

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        breadcrumb={
          <Breadcrumb
            items={[
              { label: "Home", to: "/" },
              { label: "New slice" }
            ]}
          />
        }
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-lg">
            New slice
          </h1>
        }
      />
      <p className="mb-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        Create a slice under your signed-in account with a visibility value and
        included source paths.
      </p>

      <form className="mt-8 space-y-6" onSubmit={createSlice}>
        <SlicePanel className="space-y-4">
          <div>
            <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
              Slice identity
            </h2>
            <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
              The account is resolved from your signed-in session.
            </p>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <label className="grid gap-2 text-sm font-medium text-zinc-950 dark:text-zinc-50">
              Account
              <input
                className="h-10 min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-950 px-3 font-mono text-sm text-slate-700 dark:text-zinc-300 outline-none"
                readOnly
                value={selection.isLoading ? "Loading..." : account}
              />
            </label>

            <label className="grid gap-2 text-sm font-medium text-zinc-950 dark:text-zinc-50">
              Slice name
              <input
                className="h-10 min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 font-mono text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-slate-500 dark:focus:border-zinc-500 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-500 dark:disabled:text-zinc-400"
                disabled={isBusy}
                onChange={(event) => {
                  setSliceName(event.target.value);
                  if (nameError) {
                    setNameError("");
                  }
                }}
                placeholder="payment-api"
                spellCheck={false}
                value={sliceName}
              />
              <span className="text-xs font-normal leading-5 text-slate-500 dark:text-zinc-400">
                Use lowercase letters, digits, and hyphens.
              </span>
              {nameError ? (
                <span className="text-xs font-semibold leading-5 text-rose-700 dark:text-rose-300">
                  {nameError}
                </span>
              ) : null}
            </label>
          </div>
        </SlicePanel>

        <SliceDefinitionForm
          account={account}
          disabled={isBusy}
          includedPaths={includedPaths}
          onIncludedPathsChange={setIncludedPaths}
          onVisibilityChange={setVisibility}
          visibility={visibility}
        />

        {selection.error ? (
          <SliceNotice title="Could not load your home account" tone="error">
            {getErrorMessage(selection.error)}
          </SliceNotice>
        ) : null}

        {clientErrors.length > 0 ? (
          <SliceNotice title="Fix slice definition" tone="error">
            <ul className="list-disc space-y-1 pl-5">
              {clientErrors.map((error) => (
                <li className="break-all" key={error}>
                  {error}
                </li>
              ))}
            </ul>
          </SliceNotice>
        ) : null}

        {serverError ? (
          <SliceNotice title="Could not create slice" tone="error">
            {serverError}
          </SliceNotice>
        ) : null}

        <div className="flex flex-wrap items-center gap-3">
          <button
            className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-4 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-200 dark:disabled:bg-zinc-700 disabled:text-slate-500 dark:disabled:text-zinc-400"
            disabled={selection.isLoading || isBusy}
            type="submit"
          >
            {isBusy ? "Creating..." : "Create slice"}
          </button>
        </div>
      </form>
    </section>
  );
}

function validateSliceName(value: string) {
  const name = value.trim();

  if (!name) {
    return "Slice name is required.";
  }

  if (name.length > 63) {
    return "Slice name must be 63 characters or fewer.";
  }

  if (!/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(name)) {
    return "Use lowercase letters, digits, and hyphens; do not start or end with a hyphen.";
  }

  return "";
}
