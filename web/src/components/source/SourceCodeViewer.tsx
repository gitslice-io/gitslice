import { useEffect, useMemo, useState } from "react";

import { cn } from "../../lib/cn";
import { languageFromPath } from "./sourceUtils";
import { highlightToHtml } from "./highlight";
import { MarkdownViewer } from "./MarkdownViewer";

interface SourceCodeViewerProps {
  code: string;
  fill?: boolean;
  path: string;
}

interface HighlightState {
  html: string;
  isLoading: boolean;
  error: string;
}

type MarkdownViewMode = "preview" | "raw";

export function SourceCodeViewer({
  code,
  fill = false,
  path
}: SourceCodeViewerProps) {
  const language = useMemo(() => languageFromPath(path), [path]);
  const isMarkdown = language === "markdown";
  const lineCount = useMemo(() => (code ? code.split(/\r\n|\r|\n/).length : 0), [code]);
  const [markdownViewMode, setMarkdownViewMode] =
    useState<MarkdownViewMode>("preview");
  const [highlight, setHighlight] = useState<HighlightState>({
    html: "",
    isLoading: true,
    error: ""
  });
  const shouldRenderRaw = !isMarkdown || markdownViewMode === "raw";

  useEffect(() => {
    let active = true;

    async function highlightCode() {
      if (!shouldRenderRaw) {
        setHighlight({ html: "", isLoading: false, error: "" });
        return;
      }

      setHighlight({ html: "", isLoading: true, error: "" });
      try {
        const html = await highlightToHtml(code, language);
        if (active) {
          setHighlight({ html, isLoading: false, error: "" });
        }
      } catch (error) {
        if (active) {
          setHighlight({
            html: "",
            isLoading: false,
            error: error instanceof Error ? error.message : "Unable to load syntax highlighting"
          });
        }
      }
    }

    highlightCode();

    return () => {
      active = false;
    };
  }, [code, language, shouldRenderRaw]);

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 bg-slate-50 px-4 py-3 text-xs text-slate-500">
        <div className="min-w-0 truncate font-mono text-slate-600">{path}</div>
        <div className="flex flex-wrap items-center justify-end gap-3">
          {isMarkdown ? (
            <MarkdownViewToggle
              onChange={setMarkdownViewMode}
              value={markdownViewMode}
            />
          ) : null}
          <div className="flex items-center gap-3">
            {shouldRenderRaw && highlight.isLoading && code ? (
              <span className="text-slate-400">highlighting…</span>
            ) : null}
            <span>{language}</span>
            <span>{lineCount} lines</span>
          </div>
        </div>
      </div>
      <div className={fill ? "overflow-x-auto" : "max-h-[82dvh] overflow-auto"}>
        {isMarkdown && markdownViewMode === "preview" ? (
          <MarkdownViewer source={code} />
        ) : highlight.html ? (
          <div
            className="[&_code]:block [&_code]:min-w-max [&_code]:px-4 [&_code]:py-4 [&_pre]:m-0 [&_pre]:overflow-visible [&_pre]:!bg-white [&_pre]:text-sm [&_pre]:leading-6"
            dangerouslySetInnerHTML={{ __html: highlight.html }}
          />
        ) : (
          // Show the raw text immediately so file content never waits on the
          // Shiki core load or a (potentially large) grammar-chunk download.
          // Highlighting is layered in by swapping to the rendered HTML once it
          // resolves; the plain <pre> matches its sizing to avoid layout shift.
          <pre className="min-w-max overflow-visible bg-white p-4 text-sm leading-6 text-zinc-900">
            <code>{code}</code>
          </pre>
        )}
      </div>
      {highlight.error ? (
        <div className="border-t border-slate-200 px-4 py-3 text-xs text-amber-700">
          Syntax highlighting unavailable: {highlight.error}
        </div>
      ) : null}
    </div>
  );
}

function MarkdownViewToggle({
  onChange,
  value
}: {
  onChange(value: MarkdownViewMode): void;
  value: MarkdownViewMode;
}) {
  return (
    <div className="inline-flex h-8 w-fit overflow-hidden rounded-md border border-slate-200 bg-slate-50 p-0.5">
      {(["preview", "raw"] as const).map((mode) => (
        <button
          aria-pressed={value === mode}
          className={cn(
            "rounded px-2.5 text-xs font-medium capitalize transition",
            value === mode
              ? "bg-white text-zinc-950 shadow-sm"
              : "text-slate-600 hover:text-zinc-950"
          )}
          key={mode}
          onClick={() => onChange(mode)}
          type="button"
        >
          {mode}
        </button>
      ))}
    </div>
  );
}
