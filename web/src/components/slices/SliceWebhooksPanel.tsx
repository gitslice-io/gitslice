import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import type { SliceRef, Webhook, WebhookDelivery } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { formatRelativeTime } from "./ConversationCard";
import { SliceLoadingBlock, SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

// Event names a webhook can subscribe to (design/24_webhooks.md).
export const WEBHOOK_EVENTS: { name: string; help: string }[] = [
  { name: "push", help: "a commit landed that touches this slice's paths" },
  { name: "tag.created", help: "a tag (release) was created" },
  { name: "changeset.created", help: "a changeset was opened" },
  { name: "changeset.updated", help: "a new patchset was uploaded" },
  { name: "changeset.approved", help: "a changeset was approved" },
  { name: "changeset.submitted", help: "a changeset landed" },
  { name: "changeset.abandoned", help: "a changeset was abandoned" },
  { name: "check_run.completed", help: "a check finished" }
];

const buttonClass =
  "rounded-md border border-slate-200 px-2.5 py-1 text-xs font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-50 dark:border-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-950";
const inputClass =
  "h-9 min-w-0 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700";

interface WebhookDraft {
  url: string;
  events: string[];
  secret: string;
  active: boolean;
}

const emptyDraft: WebhookDraft = { active: true, events: ["push"], secret: "", url: "" };

// The slice's webhooks, for its owners and admins: Gitslice POSTs a JSON event
// to each URL when something happens in the slice.
export function SliceWebhooksPanel({ slice }: { slice: SliceRef }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const queryKey = ["webhooks", slice.account, slice.slice];
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  const hooksQuery = useQuery({
    queryKey,
    queryFn: async () => (await api.listWebhooks({ slice })).webhooks ?? []
  });

  const refresh = () => queryClient.invalidateQueries({ queryKey });
  const onError = (err: unknown) => setError(getErrorMessage(err));

  const create = useMutation({
    mutationFn: (draft: WebhookDraft) =>
      api.createWebhook({
        active: draft.active,
        events: draft.events,
        secret: draft.secret || undefined,
        slice,
        url: draft.url.trim()
      }),
    onError,
    onMutate: () => setError(""),
    onSuccess: async () => {
      setEditing(null);
      await refresh();
    }
  });
  const update = useMutation({
    mutationFn: ({ id, draft, original }: { id: string; draft: WebhookDraft; original: Webhook }) =>
      api.updateWebhook({
        active: draft.active,
        events: draft.events,
        secret: draft.secret || undefined,
        updateEvents: true,
        url: draft.url.trim() === original.url ? undefined : draft.url.trim(),
        webhookId: id
      }),
    onError,
    onMutate: () => setError(""),
    onSuccess: async () => {
      setEditing(null);
      await refresh();
    }
  });
  const setActive = useMutation({
    mutationFn: ({ id, active }: { id: string; active: boolean }) => api.updateWebhook({ active, webhookId: id }),
    onError,
    onMutate: () => setError(""),
    onSuccess: refresh
  });
  const clearSecret = useMutation({
    mutationFn: (id: string) => api.updateWebhook({ clearSecret: true, webhookId: id }),
    onError,
    onMutate: () => setError(""),
    onSuccess: refresh
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteWebhook({ webhookId: id }),
    onError,
    onMutate: () => setError(""),
    onSuccess: refresh
  });
  const ping = useMutation({
    mutationFn: (id: string) => api.pingWebhook({ webhookId: id }),
    onError,
    onMutate: () => setError(""),
    onSuccess: async (_delivery, id) => {
      setOpen(id);
      await Promise.all([refresh(), queryClient.invalidateQueries({ queryKey: ["webhookDeliveries", id] })]);
    }
  });

  const hooks = hooksQuery.data ?? [];
  const busy = create.isPending || update.isPending || setActive.isPending || remove.isPending || clearSecret.isPending;

  return (
    <SlicePanel>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 max-w-2xl">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Webhooks</h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            Gitslice POSTs a JSON event to these URLs when something happens in this slice. With a secret, each delivery
            carries <code className="font-mono text-xs">X-Gitslice-Signature-256</code>, an HMAC-SHA256 of the body.
            Failed deliveries are retried for about a day.
          </p>
        </div>
        {editing !== "new" ? (
          <button
            className="rounded-md bg-zinc-950 px-3 py-1.5 text-sm font-semibold text-white transition hover:bg-zinc-800 disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
            disabled={busy || hooksQuery.isError}
            onClick={() => setEditing("new")}
            type="button"
          >
            Add webhook
          </button>
        ) : null}
      </div>

      {error ? (
        <div className="mt-4">
          <SliceNotice title="Webhook not saved" tone="error">
            {error}
          </SliceNotice>
        </div>
      ) : null}

      {editing === "new" ? (
        <WebhookForm
          busy={create.isPending}
          initial={emptyDraft}
          onCancel={() => setEditing(null)}
          onSubmit={(draft) => create.mutate(draft)}
          submitLabel="Add webhook"
        />
      ) : null}

      <div className="mt-4">
        {hooksQuery.isPending ? (
          <SliceLoadingBlock />
        ) : hooksQuery.isError ? (
          <SliceNotice
            title={
              isPermissionError(hooksQuery.error)
                ? "Webhooks are managed by the slice's owners and admins"
                : "Could not load webhooks"
            }
          >
            {getErrorMessage(hooksQuery.error)}
          </SliceNotice>
        ) : hooks.length === 0 ? (
          <p className="text-sm text-slate-500 dark:text-zinc-400">No webhooks yet.</p>
        ) : (
          <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
            {hooks.map((hook) => {
              const id = hook.id ?? "";
              if (editing === id) {
                return (
                  <li className="px-4 py-3" key={id}>
                    <WebhookForm
                      busy={update.isPending}
                      initial={{ active: hook.active ?? true, events: hook.events ?? [], secret: "", url: hook.url ?? "" }}
                      onCancel={() => setEditing(null)}
                      onSubmit={(draft) => update.mutate({ draft, id, original: hook })}
                      secretHint={hook.hasSecret ? "Leave empty to keep the current secret." : undefined}
                      submitLabel="Save webhook"
                    />
                  </li>
                );
              }
              return (
                <li className="px-4 py-3" key={id}>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                    <span
                      className={cn(
                        "h-2 w-2 shrink-0 rounded-full",
                        !hook.active
                          ? "bg-slate-300 dark:bg-zinc-600"
                          : deliveryTone(hook.lastDelivery) === "bad"
                            ? "bg-rose-500"
                            : "bg-emerald-500"
                      )}
                      title={hook.active ? "active" : "inactive"}
                    />
                    <code className="min-w-0 flex-1 truncate font-mono text-sm text-zinc-950 dark:text-zinc-50" title={displayWebhookURL(hook.url)}>
                      {displayWebhookURL(hook.url)}
                    </code>
                    <div className="flex flex-wrap gap-1.5">
                      <button className={buttonClass} disabled={ping.isPending} onClick={() => ping.mutate(id)} type="button">
                        {ping.isPending && ping.variables === id ? "Pinging..." : "Ping"}
                      </button>
                      <button className={buttonClass} onClick={() => setOpen(open === id ? null : id)} type="button">
                        {open === id ? "Hide deliveries" : "Deliveries"}
                      </button>
                      <button className={buttonClass} disabled={busy} onClick={() => setEditing(id)} type="button">
                        Edit
                      </button>
                      <button
                        className={buttonClass}
                        disabled={busy}
                        onClick={() => setActive.mutate({ active: !hook.active, id })}
                        type="button"
                      >
                        {hook.active ? "Disable" : "Enable"}
                      </button>
                      {hook.hasSecret ? (
                        <button className={buttonClass} disabled={busy} onClick={() => clearSecret.mutate(id)} type="button">
                          Remove secret
                        </button>
                      ) : null}
                      <button
                        className={cn(buttonClass, "text-rose-700 hover:bg-rose-50 dark:text-rose-300 dark:hover:bg-rose-950/30")}
                        disabled={busy}
                        onClick={() => {
                          if (window.confirm(`Delete the webhook to ${displayWebhookURL(hook.url)}? Its delivery history goes too.`)) {
                            remove.mutate(id);
                          }
                        }}
                        type="button"
                      >
                        Delete
                      </button>
                    </div>
                  </div>
                  <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 pl-5 text-xs text-slate-500 dark:text-zinc-400">
                    <span>{(hook.events ?? []).map((event) => (event === "*" ? "all events" : event)).join(", ")}</span>
                    <span>{hook.active ? "active" : "inactive"}</span>
                    <span>{hook.hasSecret ? "signed" : "unsigned"}</span>
                    <span>
                      {hook.lastDelivery ? (
                        <>
                          last delivery {formatRelativeTime(hook.lastDelivery.createdAt)}:{" "}
                          <DeliveryStatus delivery={hook.lastDelivery} />
                        </>
                      ) : (
                        "never delivered"
                      )}
                    </span>
                  </div>
                  {open === id ? <WebhookDeliveries webhookId={id} /> : null}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </SlicePanel>
  );
}

function WebhookForm({
  busy,
  initial,
  onCancel,
  onSubmit,
  secretHint,
  submitLabel
}: {
  busy: boolean;
  initial: WebhookDraft;
  onCancel(): void;
  onSubmit(draft: WebhookDraft): void;
  secretHint?: string;
  submitLabel: string;
}) {
  const [draft, setDraft] = useState<WebhookDraft>(initial);
  const [problem, setProblem] = useState("");
  const all = draft.events.includes("*");

  function toggle(name: string) {
    setDraft((current) => ({
      ...current,
      events: current.events.includes(name)
        ? current.events.filter((event) => event !== name)
        : [...current.events.filter((event) => event !== "*"), name]
    }));
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!/^https:\/\//i.test(draft.url.trim())) {
      setProblem("The URL must start with https://");
      return;
    }
    if (draft.events.length === 0) {
      setProblem("Choose at least one event.");
      return;
    }
    setProblem("");
    onSubmit(draft);
  }

  return (
    <form aria-label={submitLabel} className="mt-4 grid gap-4 rounded-lg border border-slate-200 p-4 dark:border-zinc-800" onSubmit={submit}>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Payload URL
        <input
          className={cn(inputClass, "font-mono")}
          disabled={busy}
          onChange={(event) => setDraft({ ...draft, url: event.target.value })}
          placeholder="https://example.com/gitslice-webhook"
          spellCheck={false}
          type="url"
          value={draft.url}
        />
      </label>
      <fieldset className="grid gap-2">
        <legend className="mb-1 text-xs font-medium text-slate-600 dark:text-zinc-400">Events</legend>
        <label className="flex items-center gap-2 text-sm text-zinc-900 dark:text-zinc-100">
          <input
            checked={all}
            disabled={busy}
            onChange={() => setDraft({ ...draft, events: all ? [] : ["*"] })}
            type="checkbox"
          />
          All events
        </label>
        <div className="grid gap-1.5 sm:grid-cols-2">
          {WEBHOOK_EVENTS.map((event) => (
            <label className="flex items-start gap-2 text-sm text-zinc-900 dark:text-zinc-100" key={event.name}>
              <input
                checked={all || draft.events.includes(event.name)}
                className="mt-1"
                disabled={busy || all}
                onChange={() => toggle(event.name)}
                type="checkbox"
              />
              <span>
                <code className="font-mono text-xs">{event.name}</code>
                <span className="block text-xs text-slate-500 dark:text-zinc-400">{event.help}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Secret (optional)
        <input
          autoComplete="new-password"
          className={cn(inputClass, "font-mono")}
          disabled={busy}
          onChange={(event) => setDraft({ ...draft, secret: event.target.value })}
          placeholder={secretHint ? "unchanged" : "used to sign deliveries"}
          type="password"
          value={draft.secret}
        />
        {secretHint ? <span className="font-normal text-slate-500 dark:text-zinc-400">{secretHint}</span> : null}
      </label>
      <label className="flex items-center gap-2 text-sm text-zinc-900 dark:text-zinc-100">
        <input checked={draft.active} disabled={busy} onChange={() => setDraft({ ...draft, active: !draft.active })} type="checkbox" />
        Active
      </label>
      {problem ? <p className="text-sm text-rose-700 dark:text-rose-300">{problem}</p> : null}
      <div className="flex flex-wrap gap-2">
        <button
          className="rounded-md bg-zinc-950 px-3 py-1.5 text-sm font-semibold text-white transition hover:bg-zinc-800 disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
          disabled={busy}
          type="submit"
        >
          {busy ? "Saving..." : submitLabel}
        </button>
        <button className={buttonClass} disabled={busy} onClick={onCancel} type="button">
          Cancel
        </button>
      </div>
    </form>
  );
}

function deliveryTone(delivery?: WebhookDelivery) {
  if (!delivery) return "none";
  if (delivery.status === "succeeded") return "good";
  if (delivery.status === "pending" && (delivery.attempts ?? 0) === 0) return "none";
  return "bad";
}

function DeliveryStatus({ delivery }: { delivery: WebhookDelivery }) {
  const tone = deliveryTone(delivery);
  const parts = [delivery.status ?? "unknown"];
  if (delivery.responseStatus) parts.push(`HTTP ${delivery.responseStatus}`);
  if ((delivery.attempts ?? 0) > 1) parts.push(`${delivery.attempts} attempts`);
  return (
    <span
      className={cn(
        "font-medium",
        tone === "good" && "text-emerald-700 dark:text-emerald-300",
        tone === "bad" && "text-rose-700 dark:text-rose-300"
      )}
      title={delivery.error || undefined}
    >
      {parts.join(", ")}
    </span>
  );
}

function WebhookDeliveries({ webhookId }: { webhookId: string }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [shown, setShown] = useState<string | null>(null);
  const deliveriesQuery = useQuery({
    queryKey: ["webhookDeliveries", webhookId],
    queryFn: async () => (await api.listWebhookDeliveries({ limit: 30, webhookId })).deliveries ?? []
  });
  const redeliver = useMutation({
    mutationFn: (deliveryId: string) => api.redeliverWebhookDelivery({ deliveryId }),
    onSuccess: async (delivery) => {
      setShown(delivery.id ?? null);
      await queryClient.invalidateQueries({ queryKey: ["webhookDeliveries", webhookId] });
    }
  });

  if (deliveriesQuery.isPending) {
    return <p className="mt-3 pl-5 text-xs text-slate-500 dark:text-zinc-400">Loading deliveries...</p>;
  }
  if (deliveriesQuery.isError) {
    return <p className="mt-3 pl-5 text-xs text-rose-700 dark:text-rose-300">{getErrorMessage(deliveriesQuery.error)}</p>;
  }
  const deliveries = deliveriesQuery.data ?? [];
  if (deliveries.length === 0) {
    return <p className="mt-3 pl-5 text-xs text-slate-500 dark:text-zinc-400">No deliveries yet. Ping it to send one.</p>;
  }
  return (
    <div className="mt-3 pl-5">
      {redeliver.isError ? (
        <p className="mb-2 text-xs text-rose-700 dark:text-rose-300">{getErrorMessage(redeliver.error)}</p>
      ) : null}
      <ul aria-label="Recent deliveries" className="divide-y divide-slate-100 rounded-md border border-slate-200 text-xs dark:divide-zinc-800 dark:border-zinc-800">
        {deliveries.map((delivery) => {
          const id = delivery.id ?? "";
          return (
            <li key={id}>
              <button
                aria-expanded={shown === id}
                className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-left hover:bg-slate-50 dark:hover:bg-zinc-950"
                onClick={() => setShown(shown === id ? null : id)}
                type="button"
              >
                <code className="font-mono text-zinc-900 dark:text-zinc-100">{delivery.event}</code>
                <DeliveryStatus delivery={delivery} />
                <span className="text-slate-500 dark:text-zinc-400">{formatRelativeTime(delivery.createdAt)}</span>
                {delivery.durationMs ? (
                  <span className="text-slate-500 dark:text-zinc-400">{String(delivery.durationMs)}ms</span>
                ) : null}
                {delivery.status === "pending" && delivery.nextAttemptAt && (delivery.attempts ?? 0) > 0 ? (
                  <span className="text-slate-500 dark:text-zinc-400">retry {formatRelativeTime(delivery.nextAttemptAt).replace(" ago", "")}</span>
                ) : null}
              </button>
              {shown === id ? (
                <div className="grid gap-3 border-t border-slate-100 px-3 py-3 dark:border-zinc-800">
                  {delivery.error ? <p className="text-rose-700 dark:text-rose-300">{delivery.error}</p> : null}
                  <p className="text-slate-500 dark:text-zinc-400">
                    Delivery <code className="font-mono">{id}</code>, event <code className="font-mono">{delivery.eventId}</code>
                  </p>
                  <BodyBlock label="Request body" text={prettyJSON(delivery.requestBody)} />
                  {delivery.responseBody ? <BodyBlock label="Response body" text={delivery.responseBody} /> : null}
                  <div>
                    <button className={buttonClass} disabled={redeliver.isPending} onClick={() => redeliver.mutate(id)} type="button">
                      {redeliver.isPending ? "Redelivering..." : "Redeliver"}
                    </button>
                  </div>
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function BodyBlock({ label, text }: { label: string; text: string }) {
  return (
    <div className="min-w-0">
      <p className="mb-1 font-medium text-slate-600 dark:text-zinc-400">{label}</p>
      <pre className="max-h-72 overflow-auto rounded-md bg-slate-50 p-2 font-mono text-[11px] leading-5 text-zinc-900 dark:bg-zinc-950 dark:text-zinc-100">
        {text}
      </pre>
    </div>
  );
}

// displayWebhookURL hides query values, which often carry a token; Edit shows
// the whole URL.
export function displayWebhookURL(raw?: string) {
  if (!raw) return "";
  const at = raw.indexOf("?");
  if (at < 0) return raw;
  const query = raw
    .slice(at + 1)
    .split("&")
    .map((pair) => `${pair.split("=")[0]}=...`)
    .join("&");
  return `${raw.slice(0, at)}?${query}`;
}

function isPermissionError(error: unknown) {
  const status = (error as { status?: number } | null)?.status;
  return status === 403 || /permission/i.test(getErrorMessage(error));
}

function prettyJSON(text?: string) {
  if (!text) return "";
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}
