import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import { useApi } from "../api/useApi";
import { BrandMark } from "../components/BrandMark";
import { ThemeToggle } from "../components/ThemeToggle";
import { Button } from "../components/ui/Button";
import { Input } from "../components/ui/Input";
import { authStatusKey, usernameAvailableQuery } from "../queries/auth";

export function ChooseUsernamePage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const [username, setUsername] = useState("");
  const [debouncedUsername, setDebouncedUsername] = useState("");
  const [submitError, setSubmitError] = useState("");

  useEffect(() => {
    const timeoutId = window.setTimeout(() => {
      setDebouncedUsername(username.trim());
    }, 400);

    return () => window.clearTimeout(timeoutId);
  }, [username]);

  const trimmedUsername = username.trim();
  const availabilityQuery = useQuery({
    ...usernameAvailableQuery(api, debouncedUsername),
    enabled: debouncedUsername.length > 0
  });

  const chooseUsernameMutation = useMutation({
    mutationFn: (selectedUsername: string) =>
      api.chooseUsername({ username: selectedUsername }),
    onError: (error) => {
      setSubmitError(
        error instanceof Error ? error.message : "Could not set username"
      );
    },
    onMutate: () => {
      setSubmitError("");
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: authStatusKey });
    }
  });

  const availability = availabilityQuery.data;
  const hasCurrentAvailability =
    trimmedUsername.length > 0 && trimmedUsername === debouncedUsername;
  const isChecking =
    trimmedUsername.length > 0 &&
    (trimmedUsername !== debouncedUsername || availabilityQuery.isFetching);
  const isAvailable =
    hasCurrentAvailability && availability?.available === true;
  const normalized = availability?.normalized?.trim() ?? "";
  const normalizedDiffers =
    normalized.length > 0 && normalized !== debouncedUsername;
  const canSubmit =
    trimmedUsername.length > 0 &&
    isAvailable &&
    !isChecking &&
    !chooseUsernameMutation.isPending;

  function submitUsername(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitError("");

    if (!canSubmit) {
      return;
    }

    chooseUsernameMutation.mutate(trimmedUsername);
  }

  return (
    <main className="grid min-h-[100dvh] place-items-center bg-slate-50 p-4 text-zinc-900 transition-colors duration-200 dark:bg-zinc-950 dark:text-zinc-100 sm:p-6">
      <section className="w-full max-w-md overflow-hidden rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/20 sm:p-6">
        <div className="flex items-center justify-between gap-4">
          <p className="flex items-center gap-2 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
            <BrandMark className="size-5" />
            Gitslice
          </p>
          <ThemeToggle />
        </div>
        <h1 className="mt-2 text-xl font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-2xl">
          Choose your username
        </h1>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Use at least four lowercase letters, numbers, or hyphens. This becomes
          your account namespace, and your files live under{" "}
          <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:bg-zinc-800 dark:text-zinc-300">
            /&lt;username&gt;
          </code>
          .
        </p>

        <form className="mt-6 space-y-5" onSubmit={submitUsername}>
          <label className="grid gap-2 text-sm font-medium text-zinc-950 dark:text-zinc-100">
            Username
            <Input
              autoCapitalize="none"
              autoComplete="username"
              autoFocus
              className="h-10"
              disabled={chooseUsernameMutation.isPending}
              maxLength={63}
              minLength={4}
              monospace
              onChange={(event) => {
                setUsername(event.target.value);
                if (submitError) {
                  setSubmitError("");
                }
              }}
              placeholder="payment-api"
              spellCheck={false}
              value={username}
            />
            <span className="text-xs font-normal leading-5 text-slate-500 dark:text-zinc-500">
              At least four characters. Lowercase letters, numbers, and hyphens
              only.
            </span>
          </label>

          <div aria-live="polite" className="min-h-5 text-xs leading-5">
            {trimmedUsername && isChecking ? (
              <p className="font-semibold text-slate-600 dark:text-zinc-400">
                Checking…
              </p>
            ) : null}

            {trimmedUsername &&
            !isChecking &&
            hasCurrentAvailability &&
            availabilityQuery.isError ? (
              <p className="font-semibold text-rose-700 dark:text-rose-300">
                {availabilityQuery.error instanceof Error
                  ? availabilityQuery.error.message
                  : "Could not check username."}
              </p>
            ) : null}

            {trimmedUsername &&
            !isChecking &&
            hasCurrentAvailability &&
            !availabilityQuery.isError &&
            availability?.available === false ? (
              <p className="font-semibold text-rose-700 dark:text-rose-300">
                {availability.reason || "Username is not available."}
              </p>
            ) : null}

            {trimmedUsername && !isChecking && isAvailable ? (
              <p className="font-semibold text-emerald-700 dark:text-emerald-300">
                {normalizedDiffers
                  ? `${debouncedUsername} is available and will be saved as ${normalized}.`
                  : `${normalized || debouncedUsername} is available.`}
              </p>
            ) : null}
          </div>

          {submitError ? (
            <div className="rounded-lg border border-rose-200 bg-rose-50 p-3 text-sm font-semibold leading-6 text-rose-900 dark:border-rose-900/60 dark:bg-rose-950/30 dark:text-rose-200">
              {submitError}
            </div>
          ) : null}

          <Button
            className="w-full py-2"
            disabled={!canSubmit}
            size="md"
            type="submit"
            variant="primary"
          >
            {chooseUsernameMutation.isPending ? "Saving..." : "Continue"}
          </Button>
        </form>
      </section>
    </main>
  );
}
