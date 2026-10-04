import { useEffect, useState } from "react";

import { JsonTree } from "./JsonTree";
import { PreviewError } from "./JsonViewer";

// A tree preview for YAML and TOML, parsed in the browser. The parsers load on
// first use; they are not part of the main bundle.

type State = { status: "loading" } | { status: "error"; message: string } | { status: "ready"; value: unknown };

export async function parseStructured(format: "yaml" | "toml", source: string): Promise<unknown> {
  if (format === "toml") {
    const { parse } = await import("smol-toml");
    return parse(source);
  }
  const { parseAllDocuments } = await import("yaml");
  const documents = parseAllDocuments(source);
  for (const document of documents) {
    if (document.errors.length > 0) {
      throw document.errors[0];
    }
  }
  const values = documents.map((document) => document.toJS() as unknown);
  return values.length === 1 ? values[0] : values;
}

export function StructuredViewer({ format, source }: { format: "yaml" | "toml"; source: string }) {
  const [state, setState] = useState<State>({ status: "loading" });

  useEffect(() => {
    let active = true;
    setState({ status: "loading" });
    parseStructured(format, source).then(
      (value) => active && setState({ status: "ready", value }),
      (error) => active && setState({ status: "error", message: error instanceof Error ? error.message.split("\n")[0] : "parse error" })
    );
    return () => {
      active = false;
    };
  }, [format, source]);

  if (state.status === "loading") {
    return <p className="p-4 text-sm text-slate-500 dark:text-zinc-400">Parsing…</p>;
  }
  if (state.status === "error") {
    return <PreviewError format={format === "yaml" ? "YAML" : "TOML"} message={state.message} />;
  }
  if (state.value === null || state.value === undefined) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This file is empty.</p>;
  }
  return <JsonTree value={state.value} />;
}
