import { cn } from "../../lib/cn";
import { humanizeStatus, statusClass } from "./status";

export function StatusBadge({ status }: { status?: string }) {
  const label = humanizeStatus(status);
  return (
    <span
      className={cn(
        "inline-flex rounded-md border px-2 py-0.5 text-[11px] font-semibold md:py-1 md:text-xs",
        statusClass(status)
      )}
      title={status || "unknown"}
    >
      {label}
    </span>
  );
}