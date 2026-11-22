import { CopyButton } from "../../components/ui/CopyButton";

// label names the command for someone who cannot see which heading it sits under.
export function CopyRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="mt-2 flex min-w-0 items-stretch gap-2">
      <input
        aria-label={label}
        className="min-w-0 flex-1 rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-950 px-2.5 py-2 font-mono text-xs text-zinc-950 dark:text-zinc-50"
        onFocus={(event) => event.currentTarget.select()}
        readOnly
        value={value}
      />
      <CopyButton
        ariaLabel={`Copy ${label}`}
        className="px-3 py-2"
        resetMs={1500}
        size="sm"
        value={value}
        variant="primary"
      />
    </div>
  );
}
