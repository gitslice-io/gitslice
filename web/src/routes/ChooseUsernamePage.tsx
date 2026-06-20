import {
  useMutation,
  useQuery,
  useQueryClient
} from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import { useApi } from "../api/useApi";
import { Badge, Button, Card, Input, PageHeader } from "../components/ui";

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
    enabled: debouncedUsername.length > 0,
    queryKey: ["usernameAvailable", debouncedUsername],
    queryFn: () => api.checkUsernameAvailable({ username: debouncedUsername })
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
      await queryClient.invalidateQueries({ queryKey: ["authStatus"] });
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
  const inputHasError =
    trimmedUsername.length > 0 &&
    !isChecking &&
    hasCurrentAvailability &&
    (availabilityQuery.isError || availability?.available === false);

  function submitUsername(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitError("");

    if (!canSubmit) {
      return;
    }

    chooseUsernameMutation.mutate(trimmedUsername);
  }

  return (
    <main className="grid min-h-[100dvh] place-items-center bg-surface px-4 py-8 text-on-surface sm:px-6">
      <Card as="section" className="w-full max-w-lg" level="low" padding="lg">
        <PageHeader
          className="py-0"
          description={
            <>
              Use lowercase letters, numbers, and hyphens. This becomes your
              account namespace, and your files live under{" "}
              <code className="rounded-sm bg-surface-container-high px-1.5 py-0.5 font-mono text-xs text-on-surface">
                /&lt;username&gt;
              </code>
              .
            </>
          }
          eyebrow="Gitslice"
          title="Choose your username"
        />

        <form className="mt-6 space-y-5" onSubmit={submitUsername}>
          <label className="grid gap-2 font-label text-sm font-semibold text-on-surface">
            Username
            <Input
              autoCapitalize="none"
              autoComplete="username"
              autoFocus
              disabled={chooseUsernameMutation.isPending}
              error={inputHasError}
              className="font-mono"
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
            <span className="font-sans text-xs font-normal leading-5 text-on-surface-muted">
              Lowercase letters, numbers, and hyphens only.
            </span>
          </label>

          <div
            aria-live="polite"
            className="min-h-8 space-y-2 text-xs leading-5"
          >
            {trimmedUsername && isChecking ? (
              <Badge>Checking</Badge>
            ) : null}

            {trimmedUsername &&
            !isChecking &&
            hasCurrentAvailability &&
            availabilityQuery.isError ? (
              <p className="font-semibold text-rose-800">
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
              <p className="font-semibold text-rose-800">
                {availability.reason || "Username is not available."}
              </p>
            ) : null}

            {trimmedUsername && !isChecking && isAvailable ? (
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="primary">Available</Badge>
                <span className="font-semibold text-on-surface-variant">
                  {normalizedDiffers
                    ? `${debouncedUsername} will be saved as ${normalized}.`
                    : `${normalized || debouncedUsername} is available.`}
                </span>
              </div>
            ) : null}
          </div>

          {submitError ? (
            <Card
              as="div"
              className="bg-rose-50 text-sm font-semibold leading-6 text-rose-900"
              level="high"
              padding="sm"
            >
              {submitError}
            </Card>
          ) : null}

          <Button
            className="w-full"
            disabled={!canSubmit}
            size="lg"
            type="submit"
          >
            {chooseUsernameMutation.isPending ? "Saving..." : "Continue"}
          </Button>
        </form>
      </Card>
    </main>
  );
}
