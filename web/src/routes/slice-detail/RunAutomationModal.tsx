import { useEffect, useId, useRef, useState, type FormEvent } from "react";

import type { TriggerDefinition } from "../../api/types";
import { Button } from "../../components/ui/Button";

export interface RunAutomationModalProps {
  automation: TriggerDefinition;
  busy: boolean;
  error?: string;
  onClose(): void;
  onSubmit(inputs: Record<string, string>): void;
  open: boolean;
}

export function needsRunModal(automation: TriggerDefinition): boolean {
  return (
    (automation.inputs ?? []).length > 0 ||
    Boolean(automation.manual?.confirm) ||
    (automation.secrets ?? []).length > 0 ||
    automation.action === "bash"
  );
}

// RunAutomationModal collects typed inputs, displays the security boundary
// (secrets sealed vs revealed, egress tier, runtime), and enforces any
// manual.confirm guard before firing a manual automation.
export function RunAutomationModal({
  automation,
  busy,
  error,
  onClose,
  onSubmit,
  open
}: RunAutomationModalProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const baseId = useId();
  const specs = automation.inputs ?? [];
  const [values, setValues] = useState<Record<string, string>>({});
  const [confirmed, setConfirmed] = useState(false);

  useEffect(() => {
    if (!open) return;
    const initial: Record<string, string> = {};
    for (const spec of specs) {
      const name = spec.name ?? "";
      if (!name) continue;
      if (spec.defaultValue !== undefined && spec.defaultValue !== "") {
        initial[name] = spec.defaultValue;
      } else if (spec.type === "boolean") {
        initial[name] = "false";
      } else if (spec.type === "choice" && (spec.options ?? []).length > 0) {
        initial[name] = spec.options?.[0] ?? "";
      } else {
        initial[name] = "";
      }
    }
    setValues(initial);
    setConfirmed(false);
  }, [open, automation.id]);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (open) {
      if (!dialog.open) {
        if (typeof dialog.showModal === "function") {
          dialog.showModal();
        } else {
          dialog.setAttribute("open", "");
        }
      }
    } else if (dialog.open || dialog.hasAttribute("open")) {
      if (typeof dialog.close === "function") {
        dialog.close();
      } else {
        dialog.removeAttribute("open");
      }
    }
  }, [open]);

  if (!open) {
    return null;
  }

  const secrets = automation.secrets ?? [];
  const revealed = new Set(automation.reveal ?? []);
  const network = automation.network ?? [];
  const confirmPrompt = automation.manual?.confirm ?? "";
  const roles = automation.manual?.roles ?? [];
  const adminOnly = roles.length > 0 && !roles.includes("writer");
  const requireHuman = Boolean(automation.manual?.requireHuman);

  const egressLabel =
    automation.action === "bash"
      ? automation.egress === "direct"
        ? "Direct internet (TCP/UDP)"
        : automation.egress === "http-gate" || network.length > 0
          ? `HTTPS gate (${network.join(", ")})`
          : "Offline (no internet)"
      : network.length > 0
        ? `HTTPS gate (${network.join(", ")})`
        : automation.action === "http"
          ? automation.httpUrl || "HTTP webhook"
          : "Offline";

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (confirmPrompt && !confirmed) {
      return;
    }
    const payload: Record<string, string> = {};
    for (const spec of specs) {
      const name = spec.name ?? "";
      if (!name) continue;
      const val = values[name] ?? "";
      if (val !== "" || spec.type === "boolean") {
        payload[name] = val;
      }
    }
    onSubmit(payload);
  }

  return (
    <dialog
      aria-labelledby={`${baseId}-title`}
      className="fixed inset-0 z-[60] m-0 h-dvh max-h-none w-screen max-w-none border-0 bg-transparent p-4 backdrop:bg-black/45"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (cardRef.current && !cardRef.current.contains(event.target as Node)) {
          onClose();
        }
      }}
      ref={dialogRef}
    >
      <div className="flex min-h-full items-center justify-center">
        <div
          className="w-full max-w-lg rounded-lg border border-slate-200 bg-white text-zinc-950 shadow-xl dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-50"
          ref={cardRef}
        >
          <div className="border-b border-slate-200 px-4 py-3.5 dark:border-zinc-800">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <h3 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50" id={`${baseId}-title`}>
                  Run automation
                </h3>
                <p className="mt-0.5 break-all font-mono text-xs text-slate-600 dark:text-zinc-300">
                  {automation.id}
                </p>
                {automation.description ? (
                  <p className="mt-1 text-xs text-slate-600 dark:text-zinc-400">{automation.description}</p>
                ) : null}
              </div>
              <button
                className="shrink-0 rounded-md border border-slate-300 px-2 py-1 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-950"
                onClick={onClose}
                type="button"
              >
                Cancel
              </button>
            </div>
          </div>

          <form className="grid gap-4 px-4 py-4" onSubmit={handleSubmit}>
            <div className="rounded-md border border-slate-200 bg-slate-50 px-3 py-2.5 text-xs dark:border-zinc-800 dark:bg-zinc-950">
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="font-semibold text-zinc-900 dark:text-zinc-100">
                  {automation.action === "bash"
                    ? `Bash container (${automation.bash?.runtime || "base"} · ${automation.bash?.size || "basic"})`
                    : automation.action}
                </span>
                {adminOnly ? (
                  <span className="rounded border border-amber-300 bg-amber-50 px-1.5 py-px text-[11px] font-medium text-amber-900 dark:border-amber-800/80 dark:bg-amber-950/50 dark:text-amber-200">
                    Admin only
                  </span>
                ) : null}
                {requireHuman ? (
                  <span className="rounded border border-sky-200 bg-sky-50 px-1.5 py-px text-[11px] font-medium text-sky-900 dark:border-sky-900/60 dark:bg-sky-950/50 dark:text-sky-200">
                    Human only
                  </span>
                ) : null}
              </div>
              <p className="mt-1 text-slate-600 dark:text-zinc-400">
                Runs landed <span className="font-mono">refs/global/main</span> definition in{" "}
                <span className="font-mono">{automation.folder}</span> · Egress: {egressLabel}
              </p>
              {secrets.length > 0 ? (
                <div className="mt-2 flex flex-wrap items-center gap-1.5">
                  <span className="text-slate-500 dark:text-zinc-400">Secrets:</span>
                  {secrets.map((name) => (
                    <span
                      className="rounded border border-slate-200 bg-white px-1.5 py-px font-mono text-[11px] text-zinc-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-200"
                      key={name}
                    >
                      {name}{" "}
                      <span className="font-sans text-[10px] text-slate-500 dark:text-zinc-400">
                        ({revealed.has(name) ? "revealed" : "sealed"})
                      </span>
                    </span>
                  ))}
                </div>
              ) : null}
            </div>

            {specs.length > 0 ? (
              <div className="grid gap-3">
                {specs.map((spec) => {
                  const name = spec.name ?? "";
                  const inputId = `${baseId}-input-${name}`;
                  const value = values[name] ?? "";
                  return (
                    <div className="grid gap-1" key={name}>
                      {spec.type === "boolean" ? (
                        <label className="flex items-center gap-2 text-xs font-medium text-zinc-900 dark:text-zinc-100" htmlFor={inputId}>
                          <input
                            checked={value === "true"}
                            className="h-4 w-4 rounded border-slate-300 dark:border-zinc-700"
                            id={inputId}
                            onChange={(e) => setValues((prev) => ({ ...prev, [name]: e.target.checked ? "true" : "false" }))}
                            type="checkbox"
                          />
                          <span className="font-mono">{name}</span>
                          {spec.description ? (
                            <span className="font-normal text-slate-500 dark:text-zinc-400">— {spec.description}</span>
                          ) : null}
                        </label>
                      ) : (
                        <>
                          <label className="text-xs font-medium text-zinc-900 dark:text-zinc-100" htmlFor={inputId}>
                            <span className="font-mono">{name}</span>
                            {spec.required ? <span className="ml-1 text-rose-600 dark:text-rose-400">*</span> : null}
                          </label>
                          {spec.description ? (
                            <p className="text-[11px] text-slate-500 dark:text-zinc-400">{spec.description}</p>
                          ) : null}
                          {spec.type === "choice" ? (
                            <select
                              className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs text-zinc-950 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-50"
                              id={inputId}
                              onChange={(e) => setValues((prev) => ({ ...prev, [name]: e.target.value }))}
                              required={Boolean(spec.required)}
                              value={value}
                            >
                              {(spec.options ?? []).map((opt) => (
                                <option key={opt} value={opt}>
                                  {opt}
                                </option>
                              ))}
                            </select>
                          ) : (
                            <input
                              className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 font-mono text-xs text-zinc-950 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-50"
                              id={inputId}
                              onChange={(e) => setValues((prev) => ({ ...prev, [name]: e.target.value }))}
                              pattern={spec.pattern || undefined}
                              placeholder={spec.defaultValue || undefined}
                              required={Boolean(spec.required)}
                              step={spec.type === "number" ? "any" : undefined}
                              type={spec.type === "number" ? "number" : "text"}
                              value={value}
                            />
                          )}
                        </>
                      )}
                    </div>
                  );
                })}
              </div>
            ) : null}

            {confirmPrompt ? (
              <label className="flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-950 dark:border-amber-800/80 dark:bg-amber-950/40 dark:text-amber-100">
                <input
                  checked={confirmed}
                  className="mt-0.5 h-4 w-4 shrink-0 rounded border-amber-400"
                  onChange={(e) => setConfirmed(e.target.checked)}
                  type="checkbox"
                />
                <span>{confirmPrompt}</span>
              </label>
            ) : null}

            {error ? (
              <p className="text-xs text-rose-700 dark:text-rose-300" role="alert">
                {error}
              </p>
            ) : null}

            <div className="flex items-center justify-end gap-2 border-t border-slate-100 pt-3 dark:border-zinc-800">
              <Button onClick={onClose} size="sm" type="button" variant="secondary">
                Cancel
              </Button>
              <Button disabled={busy || (Boolean(confirmPrompt) && !confirmed)} size="sm" type="submit" variant="primary">
                {busy ? "Starting…" : "Run automation"}
              </Button>
            </div>
          </form>
        </div>
      </div>
    </dialog>
  );
}
