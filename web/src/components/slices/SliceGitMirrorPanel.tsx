import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import { isNotFoundError } from "../../api/rpcError";
import type { SliceGitMirror, SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { ConfirmDialog } from "../ConfirmDialog";
import { Timestamp } from "../Timestamp";
import { secretButtonClass, secretDangerButtonClass, secretPrimaryButtonClass } from "../secrets/SecretForm";
import { SliceLoadingBlock, SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

const inputClass =
  "h-9 min-w-0 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:bg-slate-50 disabled:text-slate-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700 dark:disabled:bg-zinc-950";
const labelClass = "grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400";

type Draft = { url: string; branch: string; username: string; tokenSecret: string };

function draftOf(mirror: SliceGitMirror | null): Draft {
  return {
    url: mirror?.url ?? "",
    branch: mirror?.branch ?? "",
    username: mirror?.username ?? "",
    tokenSecret: mirror?.tokenSecret ?? ""
  };
}

export const sliceGitMirrorQueryKey = (slice: SliceRef) => ["sliceGitMirror", slice.account ?? "", slice.slice ?? ""];

/**
 * The slice's Git mirror (design/35_git_mirrors.md): the server pushes every
 * landing and tag to a repository elsewhere, such as GitHub. The token is one
 * of the slice's secrets, chosen by name.
 */
export function SliceGitMirrorPanel({ slice }: { slice: SliceRef }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const queryKey = sliceGitMirrorQueryKey(slice);
  const [draft, setDraft] = useState<Draft>(draftOf(null));
  const [removing, setRemoving] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const mirrorQuery = useQuery({
    queryKey,
    queryFn: async () => {
      try {
        return await api.getSliceGitMirror({ slice });
      } catch (err) {
        if (isNotFoundError(err)) {
          return null;
        }
        throw err;
      }
    },
    // Follow a push while one is waiting.
    refetchInterval: (query) => (query.state.data?.pending ? 3000 : false)
  });
  const secretsQuery = useQuery({
    queryKey: ["sliceSecrets", slice.account ?? "", slice.slice ?? ""],
    queryFn: async () => (await api.listSliceSecrets({ slice })).secrets ?? []
  });
  const mirror = mirrorQuery.data ?? null;
  useEffect(() => {
    setDraft(draftOf(mirror));
    // Reset the form when the saved setting changes, not on every poll.
  }, [mirror?.url, mirror?.branch, mirror?.username, mirror?.tokenSecret]);

  const refresh = () => queryClient.invalidateQueries({ queryKey });
  const onError = (err: unknown) => {
    setMessage("");
    setError(getErrorMessage(err));
  };
  const save = useMutation({
    mutationFn: () =>
      api.setSliceGitMirror({
        slice,
        url: draft.url.trim(),
        branch: draft.branch.trim(),
        username: draft.username.trim(),
        tokenSecret: draft.tokenSecret
      }),
    onSuccess: async (saved) => {
      setError("");
      setMessage("Saved. The first push starts in a moment.");
      queryClient.setQueryData(queryKey, saved);
      await refresh();
    },
    onError
  });
  const push = useMutation({
    mutationFn: () => api.pushSliceGitMirror({ slice }),
    onSuccess: async (saved) => {
      setError("");
      setMessage("Push queued.");
      queryClient.setQueryData(queryKey, saved);
      await refresh();
    },
    onError
  });
  const remove = useMutation({
    mutationFn: () => api.deleteSliceGitMirror({ slice }),
    onSuccess: async () => {
      setRemoving(false);
      setError("");
      setMessage("Mirroring stopped. The remote keeps what it has.");
      queryClient.setQueryData(queryKey, null);
      await refresh();
    },
    onError: (err) => {
      setRemoving(false);
      onError(err);
    }
  });
  const busy = save.isPending || push.isPending || remove.isPending;
  const secretNames = Array.from(new Set((secretsQuery.data ?? []).map((s) => s.name ?? "").filter(Boolean))).sort();
  if (draft.tokenSecret && !secretNames.includes(draft.tokenSecret)) {
    secretNames.unshift(draft.tokenSecret);
  }
  const changed = JSON.stringify(draft) !== JSON.stringify(draftOf(mirror));

  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate();
  };

  return (
    <SlicePanel>
      <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Mirror to Git</h2>
      <p className="mt-1 max-w-2xl text-sm leading-6 text-slate-600 dark:text-zinc-400">
        Keep a copy of this slice in a Git repository elsewhere, such as GitHub. Every landing and
        tag is pushed within moments, fast-forward only. For GitHub, add a fine-grained token with
        Contents read and write on that one repository as a secret above, then choose it here.
      </p>

      {mirrorQuery.isLoading ? (
        <div className="mt-4">
          <SliceLoadingBlock />
        </div>
      ) : mirrorQuery.isError ? (
        <div className="mt-4">
          <SliceNotice title="Could not load the mirror" tone="error">
            {getErrorMessage(mirrorQuery.error)}
          </SliceNotice>
        </div>
      ) : (
        <>
          {mirror ? <MirrorStatus mirror={mirror} /> : null}
          <form className="mt-4 grid gap-3" onSubmit={submit}>
            <label className={labelClass}>
              Repository URL
              <input
                autoComplete="off"
                className={cn(inputClass, "font-mono")}
                disabled={busy}
                onChange={(event) => setDraft({ ...draft, url: event.target.value })}
                placeholder="https://github.com/owner/repo.git"
                required
                spellCheck={false}
                type="url"
                value={draft.url}
              />
            </label>
            <div className="grid gap-3 sm:grid-cols-3">
              <label className={labelClass}>
                Token secret
                <select
                  className={inputClass}
                  disabled={busy}
                  onChange={(event) => setDraft({ ...draft, tokenSecret: event.target.value })}
                  value={draft.tokenSecret}
                >
                  <option value="">None (public push)</option>
                  {secretNames.map((name) => (
                    <option key={name} value={name}>
                      {name}
                    </option>
                  ))}
                </select>
              </label>
              <label className={labelClass}>
                Branch
                <input
                  autoComplete="off"
                  className={cn(inputClass, "font-mono")}
                  disabled={busy}
                  onChange={(event) => setDraft({ ...draft, branch: event.target.value })}
                  placeholder="main"
                  spellCheck={false}
                  value={draft.branch}
                />
              </label>
              <label className={labelClass}>
                User name
                <input
                  autoComplete="off"
                  className={cn(inputClass, "font-mono")}
                  disabled={busy}
                  onChange={(event) => setDraft({ ...draft, username: event.target.value })}
                  placeholder="x-access-token"
                  spellCheck={false}
                  value={draft.username}
                />
              </label>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <button className={secretPrimaryButtonClass} disabled={busy || !draft.url.trim() || (mirror !== null && !changed)} type="submit">
                {save.isPending ? "Saving..." : mirror ? "Save changes" : "Start mirroring"}
              </button>
              {mirror ? (
                <>
                  <button className={secretButtonClass} disabled={busy} onClick={() => push.mutate()} type="button">
                    {push.isPending ? "Queuing..." : "Push now"}
                  </button>
                  <button className={secretDangerButtonClass} disabled={busy} onClick={() => setRemoving(true)} type="button">
                    Stop mirroring
                  </button>
                </>
              ) : null}
            </div>
          </form>
        </>
      )}

      {error ? (
        <div className="mt-3">
          <SliceNotice title="Could not change the mirror" tone="error">
            {error}
          </SliceNotice>
        </div>
      ) : message ? (
        <p className="mt-3 text-sm text-emerald-700 dark:text-emerald-300">{message}</p>
      ) : null}

      <ConfirmDialog
        busy={remove.isPending}
        confirmLabel="Stop mirroring"
        message={<>The server stops pushing to {mirror?.url}. The repository keeps what it has.</>}
        onCancel={() => setRemoving(false)}
        onConfirm={() => remove.mutate()}
        open={removing}
        title="Stop mirroring?"
        tone="danger"
      />
    </SlicePanel>
  );
}

function MirrorStatus({ mirror }: { mirror: SliceGitMirror }) {
  const failing = Boolean(mirror.lastError);
  return (
    <div className="mt-4 grid gap-1 rounded-md border border-slate-200 px-3 py-2 text-sm dark:border-zinc-800">
      <p className="text-zinc-800 dark:text-zinc-200">
        <span
          className={cn(
            "mr-2 inline-block size-2 rounded-full align-middle",
            failing ? "bg-rose-500" : mirror.pending ? "bg-amber-500" : "bg-emerald-500"
          )}
        />
        <span className="font-medium">{failing ? "Push failing" : mirror.pending ? "Push pending" : "Up to date"}</span>
        {mirror.lastPushedCommit ? (
          <>
            {" · last pushed "}
            <code className="font-mono text-xs">{mirror.lastPushedCommit.slice(0, 12)}</code>{" "}
            <Timestamp relative value={mirror.lastPushedAt} />
          </>
        ) : null}
      </p>
      {mirror.root ? (
        <p className="text-xs text-slate-500 dark:text-zinc-400">
          <code className="font-mono">{mirror.root}</code> becomes the repository root.
        </p>
      ) : null}
      {failing ? (
        <p className="whitespace-pre-wrap break-words font-mono text-xs text-rose-700 dark:text-rose-300">
          {mirror.lastError}
          {mirror.nextAttemptAt ? (
            <span className="font-sans">
              {" "}
              (next try <Timestamp relative value={mirror.nextAttemptAt} />)
            </span>
          ) : null}
        </p>
      ) : null}
    </div>
  );
}
