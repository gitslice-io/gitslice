import { SlicePanel } from "../../components/slices/SlicePageParts";

export function SourceSkeleton() {
  return (
    <SlicePanel>
      <div className="grid gap-3">
        <div className="h-5 w-2/5 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
        <div className="h-12 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
        <div className="h-12 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
        <div className="h-12 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
      </div>
    </SlicePanel>
  );
}

export function NavigatorSkeleton() {
  return (
    <div className="grid gap-2">
      <div className="h-9 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
      <div className="h-9 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
      <div className="h-9 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
    </div>
  );
}