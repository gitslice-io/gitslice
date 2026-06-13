import { useEffect, useMemo, useState } from "react";

import { languageFromPath } from "./sourceUtils";

interface SourceCodeViewerProps {
  code: string;
  path: string;
}

interface HighlightState {
  html: string;
  isLoading: boolean;
  error: string;
}

export function SourceCodeViewer({ code, path }: SourceCodeViewerProps) {
  const language = useMemo(() => languageFromPath(path), [path]);
  const lineCount = useMemo(() => (code ? code.split(/\r\n|\r|\n/).length : 0), [code]);
  const [highlight, setHighlight] = useState<HighlightState>({
    html: "",
    isLoading: true,
    error: ""
  });

  useEffect(() => {
    let active = true;

    async function highlightCode() {
      setHighlight({ html: "", isLoading: true, error: "" });
      try {
        const { codeToHtml } = await import("shiki");
        let html: string;
        try {
          html = await codeToHtml(code, {
            lang: language,
            theme: "github-light"
          });
        } catch {
          html = await codeToHtml(code, {
            lang: "text",
            theme: "github-light"
          });
        }

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
  }, [code, language]);

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 bg-slate-50 px-4 py-3 text-xs text-slate-500">
        <div className="min-w-0 truncate font-mono text-slate-600">{path}</div>
        <div className="flex items-center gap-3">
          <span>{language}</span>
          <span>{lineCount} lines</span>
        </div>
      </div>
      {highlight.isLoading ? (
        <div className="grid gap-2 p-4">
          <div className="h-4 w-4/5 animate-pulse rounded bg-slate-200" />
          <div className="h-4 w-2/3 animate-pulse rounded bg-slate-200" />
          <div className="h-4 w-3/4 animate-pulse rounded bg-slate-200" />
          <div className="h-4 w-1/2 animate-pulse rounded bg-slate-200" />
        </div>
      ) : (
        <div className="max-h-[70dvh] overflow-auto">
          {highlight.html ? (
            <div
              className="[&_code]:block [&_code]:min-w-max [&_code]:px-4 [&_code]:py-4 [&_pre]:m-0 [&_pre]:overflow-visible [&_pre]:!bg-white [&_pre]:text-sm [&_pre]:leading-6"
              dangerouslySetInnerHTML={{ __html: highlight.html }}
            />
          ) : (
            <pre className="min-w-max overflow-visible bg-white p-4 text-sm leading-6 text-zinc-900">
              <code>{code}</code>
            </pre>
          )}
        </div>
      )}
      {highlight.error ? (
        <div className="border-t border-slate-200 px-4 py-3 text-xs text-amber-700">
          Syntax highlighting unavailable: {highlight.error}
        </div>
      ) : null}
    </div>
  );
}
