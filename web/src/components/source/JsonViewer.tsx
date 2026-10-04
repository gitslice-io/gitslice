import { useMemo } from "react";

import { JsonTree } from "./JsonTree";

// A tree preview for JSON files. A file that does not parse says why, and the
// Raw view still shows it.
export function JsonViewer({ source }: { source: string }) {
  const parsed = useMemo(() => {
    if (source.trim() === "") {
      return { status: "empty" as const };
    }
    try {
      return { status: "ready" as const, value: JSON.parse(source) as unknown };
    } catch (error) {
      return { status: "error" as const, message: error instanceof Error ? error.message : "Invalid JSON" };
    }
  }, [source]);

  if (parsed.status === "empty") {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This file is empty.</p>;
  }
  if (parsed.status === "error") {
    return <PreviewError format="JSON" message={parsed.message} />;
  }
  return <JsonTree value={parsed.value} />;
}

export function PreviewError({ format, message }: { format: string; message: string }) {
  return (
    <div className="bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200" role="alert">
      This is not valid {format}, so there is no preview: {message}. Switch to Raw to see the file.
    </div>
  );
}
