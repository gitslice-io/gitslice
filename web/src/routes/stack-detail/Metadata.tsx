import type { ReactNode } from "react";

export function Metadata({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs font-semibold uppercase tracking-normal text-slate-500">
        {label}
      </dt>
      <dd className="mt-1 min-w-0 break-all text-sm font-medium text-zinc-950">
        {value}
      </dd>
    </div>
  );
}