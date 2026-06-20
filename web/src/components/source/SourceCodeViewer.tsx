import { useEffect, useMemo, useState } from "react";

import { cn } from "../../lib/cn";
import { Surface } from "../ui";
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
    <Surface className="overflow-hidden" level="low">
      <div className="flex flex-wrap items-center justify-between gap-3 bg-surface-container px-4 py-3 text-xs text-on-surface-muted">
        <div className="min-w-0 truncate font-mono text-on-surface-variant">
          {path}
        </div>
        <div className="flex flex-wrap items-center justify-end gap-3">
          {isMarkdown ? (
            <MarkdownViewToggle
              onChange={setMarkdownViewMode}
              value={markdownViewMode}
            />
          ) : null}
          <div className="flex items-center gap-3">
            {shouldRenderRaw && highlight.isLoading && code ? (
              <span className="text-on-surface-muted">highlighting...</span>
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
            className="[&_code]:block [&_code]:min-w-max [&_code]:px-4 [&_code]:py-4 [&_pre]:m-0 [&_pre]:overflow-visible [&_pre]:!bg-surface-container-lowest [&_pre]:text-sm [&_pre]:leading-6"
            dangerouslySetInnerHTML={{ __html: highlight.html }}
          />
        ) : (
          // Show the raw text immediately so file content never waits on the
          // Shiki core load or a (potentially large) grammar-chunk download.
          // Highlighting is layered in by swapping to the rendered HTML once it
          // resolves; the plain <pre> matches its sizing to avoid layout shift.
          <pre className="min-w-max overflow-visible bg-surface-container-lowest p-4 text-sm leading-6 text-on-surface">
            <code>{code}</code>
          </pre>
        )}
      </div>
      {highlight.error ? (
        <div className="bg-tertiary-container px-4 py-3 text-xs text-tertiary">
          Syntax highlighting unavailable: {highlight.error}
        </div>
      ) : null}
    </Surface>
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
    <div className="inline-flex h-8 w-fit overflow-hidden rounded-sm bg-surface-container-high p-0.5">
      {(["preview", "raw"] as const).map((mode) => (
        <button
          aria-pressed={value === mode}
          className={cn(
            "rounded-sm px-2.5 font-label text-xs font-semibold capitalize transition",
            value === mode
              ? "bg-surface-container-lowest text-primary"
              : "text-on-surface-variant hover:text-on-surface"
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
