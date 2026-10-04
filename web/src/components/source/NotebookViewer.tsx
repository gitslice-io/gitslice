import { useEffect, useMemo, useState } from "react";

import { sanitizeHtml, stripAnsi } from "../../lib/sanitize";
import { highlightToHtml } from "./highlight";
import { PreviewError } from "./JsonViewer";
import { MarkdownViewer } from "./MarkdownViewer";

// A Jupyter notebook (.ipynb, nbformat 4) as cells with their saved outputs.
// Outputs are what was saved in the file: nothing is executed. HTML and SVG
// outputs are sanitized, and images are only shown as images.

interface Cell {
  cell_type?: string;
  execution_count?: number | null;
  outputs?: Output[];
  source?: string | string[];
}

interface Output {
  data?: Record<string, string | string[]>;
  ename?: string;
  evalue?: string;
  name?: string;
  output_type?: string;
  text?: string | string[];
  traceback?: string[];
}

interface Notebook {
  cells?: Cell[];
  metadata?: { kernelspec?: { language?: string }; language_info?: { name?: string } };
  nbformat?: number;
}

const BASE64 = /^[A-Za-z0-9+/=\s]*$/;

export function joinText(value: string | string[] | undefined) {
  return Array.isArray(value) ? value.join("") : (value ?? "");
}

export function NotebookViewer({ source }: { source: string }) {
  const parsed = useMemo(() => {
    try {
      const notebook = JSON.parse(source) as Notebook;
      if (!notebook || !Array.isArray(notebook.cells)) {
        return { error: "no cells found; only nbformat 4 notebooks are previewed" };
      }
      return { notebook };
    } catch (error) {
      return { error: error instanceof Error ? error.message : "Invalid JSON" };
    }
  }, [source]);

  if ("error" in parsed) {
    return <PreviewError format="notebook" message={parsed.error ?? ""} />;
  }
  const { notebook } = parsed;
  const language = notebook.metadata?.language_info?.name ?? notebook.metadata?.kernelspec?.language ?? "python";
  const cells = notebook.cells ?? [];

  if (cells.length === 0) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This notebook has no cells.</p>;
  }

  return (
    <div className="divide-y divide-slate-100 dark:divide-zinc-800">
      {cells.map((cell, index) => (
        <CellView cell={cell} key={index} language={language} />
      ))}
    </div>
  );
}

function CellView({ cell, language }: { cell: Cell; language: string }) {
  const text = joinText(cell.source);
  if (cell.cell_type === "markdown") {
    return <MarkdownViewer source={text} />;
  }
  if (cell.cell_type === "raw") {
    return (
      <pre className="whitespace-pre-wrap p-4 text-sm text-zinc-900 dark:text-zinc-100">
        <code>{text}</code>
      </pre>
    );
  }
  return (
    <div className="grid grid-cols-[4.5rem_minmax(0,1fr)] gap-x-3 p-4">
      <div className="select-none pt-1 text-right font-mono text-xs text-sky-700 dark:text-sky-300">
        {cell.execution_count ? `In [${cell.execution_count}]:` : "In [ ]:"}
      </div>
      <HighlightedCode code={text} language={language} />
      {(cell.outputs ?? []).map((output, index) => (
        <OutputView key={index} output={output} />
      ))}
    </div>
  );
}

function HighlightedCode({ code, language }: { code: string; language: string }) {
  const [html, setHtml] = useState("");
  useEffect(() => {
    let active = true;
    highlightToHtml(code, language).then(
      (result) => active && setHtml(result),
      () => active && setHtml("")
    );
    return () => {
      active = false;
    };
  }, [code, language]);

  return html ? (
    <div
      className="min-w-0 overflow-x-auto rounded-md border border-slate-200 text-sm dark:border-zinc-800 [&_pre]:m-0 [&_pre]:p-3"
      dangerouslySetInnerHTML={{ __html: html }}
    />
  ) : (
    <pre className="min-w-0 overflow-x-auto rounded-md border border-slate-200 p-3 text-sm text-zinc-900 dark:border-zinc-800 dark:text-zinc-100">
      <code>{code}</code>
    </pre>
  );
}

function OutputView({ output }: { output: Output }) {
  return (
    <>
      <div />
      <div className="mt-2 min-w-0 overflow-x-auto text-sm">{renderOutput(output)}</div>
    </>
  );
}

function renderOutput(output: Output) {
  switch (output.output_type) {
    case "stream":
      return (
        <pre
          className={
            output.name === "stderr"
              ? "whitespace-pre-wrap text-rose-700 dark:text-rose-300"
              : "whitespace-pre-wrap text-zinc-900 dark:text-zinc-100"
          }
        >
          {stripAnsi(joinText(output.text))}
        </pre>
      );
    case "error":
      return (
        <pre className="whitespace-pre-wrap rounded-md bg-rose-50 p-3 text-rose-800 dark:bg-rose-950/30 dark:text-rose-200">
          {stripAnsi((output.traceback ?? [`${output.ename}: ${output.evalue}`]).join("\n"))}
        </pre>
      );
    case "execute_result":
    case "display_data":
      return <RichData data={output.data ?? {}} />;
    default:
      return null;
  }
}

// RichData shows the richest form of an output the page can show safely.
function RichData({ data }: { data: Record<string, string | string[]> }) {
  for (const type of ["image/png", "image/jpeg", "image/gif", "image/webp"]) {
    const value = joinText(data[type]).replace(/\s/g, "");
    if (value && BASE64.test(value)) {
      return <img alt="Notebook output" className="max-w-full" src={`data:${type};base64,${value}`} />;
    }
  }
  if (data["image/svg+xml"]) {
    return <SanitizedHtml html={joinText(data["image/svg+xml"])} profile="svg" />;
  }
  if (data["text/html"]) {
    return <SanitizedHtml html={joinText(data["text/html"])} profile="html" />;
  }
  if (data["text/markdown"]) {
    return <MarkdownViewer source={joinText(data["text/markdown"])} />;
  }
  if (data["text/plain"]) {
    return <pre className="whitespace-pre-wrap text-zinc-900 dark:text-zinc-100">{stripAnsi(joinText(data["text/plain"]))}</pre>;
  }
  return <span className="text-slate-500 dark:text-zinc-400">(output not shown: {Object.keys(data).join(", ") || "empty"})</span>;
}

function SanitizedHtml({ html, profile }: { html: string; profile: "html" | "svg" }) {
  const [safe, setSafe] = useState<string | null>(null);
  useEffect(() => {
    let active = true;
    sanitizeHtml(html, profile).then(
      (result) => active && setSafe(result),
      () => active && setSafe("")
    );
    return () => {
      active = false;
    };
  }, [html, profile]);
  return safe === null ? null : (
    <div className="prose prose-sm max-w-none dark:prose-invert" dangerouslySetInnerHTML={{ __html: safe }} />
  );
}
