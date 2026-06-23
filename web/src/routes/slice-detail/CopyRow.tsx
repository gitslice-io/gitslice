import { useState } from "react";

export function CopyRow({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="mt-2 flex min-w-0 items-stretch gap-2">
      <input
        className="min-w-0 flex-1 rounded-md border border-slate-300 bg-slate-50 px-2.5 py-2 font-mono text-xs text-zinc-950"
        readOnly
        value={value}
        onFocus={(event) => event.currentTarget.select()}
      />
      <button
        className="shrink-0 rounded-md bg-zinc-950 px-3 py-2 text-xs font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
        onClick={() => {
          void navigator.clipboard?.writeText(value);
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1500);
        }}
        type="button"
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}