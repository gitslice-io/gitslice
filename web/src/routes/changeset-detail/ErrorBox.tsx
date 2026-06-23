import { cn } from "../../lib/cn";

export function ErrorBox({
  className,
  message
}: {
  className?: string;
  message: string;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800",
        className
      )}
    >
      {message}
    </div>
  );
}