import { useState, type FormEvent } from "react";

import { useAppAuth } from "../auth/AppAuthProvider";
import type { AgentSessionPublic } from "../lib/agentSession";
import { Button } from "./ui/Button";
import { Input } from "./ui/Input";

interface AgentSignInFormProps {
  initialError?: string | null;
  onSignedIn?: (session: AgentSessionPublic) => void;
}

export function AgentSignInForm({ initialError = null, onSignedIn }: AgentSignInFormProps) {
  const { signInWithAgentKey } = useAppAuth();
  const [apiKey, setApiKey] = useState("");
  const [error, setError] = useState<string | null>(initialError);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = apiKey.trim();
    if (!trimmed) {
      setError("Paste an agent API key (starting with gsk_).");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const session = await signInWithAgentKey(trimmed);
      setApiKey("");
      onSignedIn?.(session);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Could not sign in as agent.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form className="space-y-4" onSubmit={(event) => void handleSubmit(event)}>
      <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
        Paste an agent API key (<code className="font-mono text-xs text-slate-800 dark:text-zinc-200">gsk_...</code>) or
        open a one-time link from{" "}
        <code className="font-mono text-xs text-slate-800 dark:text-zinc-200">gs auth web-login</code>. Gitslice
        exchanges long-lived keys for a 24-hour revocable web session stored in an{" "}
        <code className="font-mono text-xs text-slate-800 dark:text-zinc-200">HttpOnly</code> cookie.
      </p>

      <div>
        <label
          className="block text-xs font-semibold uppercase tracking-wide text-slate-600 dark:text-zinc-400"
          htmlFor="agent-api-key"
        >
          Agent API key
        </label>
        <Input
          autoComplete="off"
          autoFocus
          className="mt-1.5 w-full font-mono text-sm"
          disabled={submitting}
          id="agent-api-key"
          name="apiKey"
          onChange={(event) => setApiKey(event.target.value)}
          placeholder="gsk_..."
          spellCheck={false}
          type="password"
          value={apiKey}
        />
      </div>

      {error ? (
        <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs leading-5 text-rose-800 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-200" role="alert">
          {error}
        </p>
      ) : null}

      <Button className="w-full" disabled={submitting} size="md" type="submit" variant="primary">
        {submitting ? "Signing in..." : "Sign in as agent"}
      </Button>

      <div className="rounded-md border border-slate-200 bg-slate-50 p-3 text-xs leading-5 text-slate-600 dark:border-zinc-800 dark:bg-zinc-950/60 dark:text-zinc-400">
        <p className="font-medium text-slate-800 dark:text-zinc-200">From an agent terminal:</p>
        <code className="mt-1 block overflow-x-auto font-mono text-[11px] text-slate-700 dark:text-zinc-300">
          gs auth web-login --expires 24h
        </code>
        <p className="mt-1.5">
          Signing out revokes the web session key immediately without affecting the agent&apos;s CLI credential.
        </p>
      </div>
    </form>
  );
}
