import { useEffect, useMemo, useState, type ReactNode } from "react";

import type { SliceRef } from "../../api/types";
import { cn } from "../../lib/cn";
import { projectRoot } from "../../lib/repoLinks";
import { useMobileDetailsClass } from "../MobileDetails";
import { languageFromPath, previewKindFromPath, type PreviewKind } from "./sourceUtils";
import { highlightToHtml } from "./highlight";
import { BlameView } from "./BlameView";
import { DiagramViewer } from "./DiagramViewer";
import { JsonLinesViewer } from "./JsonLinesViewer";
import { JsonViewer } from "./JsonViewer";
import { LogViewer } from "./LogViewer";
import { MarkdownViewer } from "./MarkdownViewer";
import { NotebookViewer } from "./NotebookViewer";
import { StructuredViewer } from "./StructuredViewer";
import { TableViewer } from "./TableViewer";
import { useRepoImageLoader } from "./useRepoImageLoader";

// BlameControl lets the page own the blame state, so it can live in the URL: a
// reload, or the back button after opening a commit from the blame, comes back
// to the same view. Without it the viewer keeps the state itself and shows its
// own Blame button.
export interface BlameControl {
  on: boolean;
  // The selected change, as the short id the URL carries; empty for none.
  at: string;
  onChange(on: boolean, at: string): void;
}

interface SourceCodeViewerProps {
  blame?: BlameControl;
  code: string;
  fill?: boolean;
  path: string;
  // Where the file is in the repository, so a markdown file's relative links
  // open inside the app and its relative images load from the repository.
  repo?: {
    commitId: string;
    includedPaths?: string[];
    slice?: SliceRef;
    onOpenPath(path: string): void;
  };
}

interface HighlightState {
  html: string;
  isLoading: boolean;
  error: string;
}

type ViewMode = "preview" | "raw";

export function SourceCodeViewer({
  blame: blameControl,
  code,
  fill = false,
  path,
  repo
}: SourceCodeViewerProps) {
  const language = useMemo(() => languageFromPath(path), [path]);
  const previewKind = useMemo(() => previewKindFromPath(path), [path]);
  const lineCount = useMemo(() => (code ? code.split(/\r\n|\r|\n/).length : 0), [code]);
  const [viewMode, setViewMode] = useState<ViewMode>("preview");
  // Blame shows each line with who last changed it. It reads the file through
  // a slice, so only a file viewed in one has it; viewing it needs the raw
  // lines, not a preview.
  const [localBlame, setLocalBlame] = useState(false);
  const showBlame = blameControl ? blameControl.on : localBlame;
  const canBlame = Boolean(repo?.slice?.account && repo?.slice?.slice);
  const blaming = showBlame && canBlame;
  const [highlight, setHighlight] = useState<HighlightState>({
    html: "",
    isLoading: true,
    error: ""
  });
  const shouldRenderRaw = !previewKind || viewMode === "raw" || blaming;
  // On a phone with the page's details folded, only the Preview/Raw switch
  // stays; with nothing to switch, the bar goes too.
  const foldedOnPhone = useMobileDetailsClass();

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

  const codeBody: ReactNode = highlight.html ? (
    <div
      className="[&_code]:block [&_code]:min-w-max [&_code]:px-4 [&_code]:py-4 [&_pre]:m-0 [&_pre]:overflow-visible [&_pre]:!bg-white dark:bg-zinc-900 [&_pre]:text-sm [&_pre]:leading-6"
      dangerouslySetInnerHTML={{ __html: highlight.html }}
    />
  ) : (
    // Show the raw text immediately so file content never waits on the
    // Shiki core load or a (potentially large) grammar-chunk download.
    // Highlighting is layered in by swapping to the rendered HTML once it
    // resolves; the plain <pre> matches its sizing to avoid layout shift.
    <pre className="min-w-max overflow-visible bg-white dark:bg-zinc-900 p-4 text-sm leading-6 text-zinc-900 dark:text-zinc-100">
      <code>{code}</code>
    </pre>
  );

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900">
      <div
        className={cn(
          "flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-4 py-3 text-xs text-slate-500 dark:text-zinc-400",
          foldedOnPhone && (previewKind ? "max-lg:justify-end max-lg:py-2" : foldedOnPhone)
        )}
      >
        {/* A page that owns the blame state shows the path above already. */}
        <div className={cn("min-w-0 truncate font-mono text-slate-600 dark:text-zinc-400", foldedOnPhone, blameControl && "max-sm:hidden")}>{path}</div>
        <div className="flex flex-wrap items-center justify-end gap-3">
          {previewKind && !blaming ? (
            <ViewModeToggle onChange={setViewMode} value={viewMode} />
          ) : null}
          {canBlame && !blameControl ? <BlameToggle onChange={setLocalBlame} value={blaming} /> : null}
          <div className={cn("flex items-center gap-3", foldedOnPhone)}>
            {shouldRenderRaw && highlight.isLoading && code ? (
              <span className="text-slate-400 dark:text-zinc-500">highlighting…</span>
            ) : null}
            <span>{language}</span>
            <span>{lineCount} lines</span>
          </div>
        </div>
      </div>
      {blaming && repo?.slice ? (
        <BlameView
          code={code}
          fill={fill}
          path={path}
          repo={{ commitId: repo.commitId, slice: repo.slice }}
          selection={blameControl ? { at: blameControl.at, onChange: (at) => blameControl.onChange(true, at) } : undefined}
        >
          {codeBody}
        </BlameView>
      ) : (
        <div className={fill ? "overflow-x-auto" : "max-h-[82dvh] overflow-auto"}>
          {previewKind && viewMode === "preview" ? (
            <Preview code={code} kind={previewKind} path={path} repo={repo} />
          ) : (
            codeBody
          )}
        </div>
      )}
      {highlight.error ? (
        <div className="border-t border-slate-200 dark:border-zinc-800 px-4 py-3 text-xs text-amber-700 dark:text-amber-300">
          Syntax highlighting unavailable: {highlight.error}
        </div>
      ) : null}
    </div>
  );
}

function Preview({ code, kind, path, repo }: { code: string; kind: PreviewKind; path: string; repo: SourceCodeViewerProps["repo"] }) {
  switch (kind) {
    case "markdown":
      return <RepoMarkdown code={code} path={path} repo={repo} />;
    case "table":
      return <TableViewer path={path} source={code} />;
    case "json":
      return <JsonViewer source={code} />;
    case "jsonl":
      return <JsonLinesViewer source={code} />;
    case "structured":
      return <StructuredViewer format={path.toLowerCase().endsWith(".toml") ? "toml" : "yaml"} source={code} />;
    case "notebook":
      return <NotebookViewer source={code} />;
    case "diagram":
      return <DiagramViewer path={path} source={code} />;
    case "log":
      return <LogViewer source={code} />;
  }
}

function ViewModeToggle({
  onChange,
  value
}: {
  onChange(value: ViewMode): void;
  value: ViewMode;
}) {
  return (
    <div className="inline-flex h-8 w-fit overflow-hidden rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 p-0.5">
      {(["preview", "raw"] as const).map((mode) => (
        <button
          aria-pressed={value === mode}
          className={cn(
            "rounded px-2.5 text-xs font-medium capitalize transition",
            value === mode
              ? "bg-white dark:bg-zinc-900 text-zinc-950 dark:text-zinc-50 shadow-sm"
              : "text-slate-600 dark:text-zinc-400 hover:text-zinc-950 dark:hover:text-zinc-50"
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

// RepoMarkdown is a markdown file shown from the repository: its relative
// links open inside the app and its relative images load from the repository.
function RepoMarkdown({ code, path, repo }: { code: string; path: string; repo: SourceCodeViewerProps["repo"] }) {
  const loadImage = useRepoImageLoader(repo?.commitId ?? "", repo?.slice);
  if (!repo) {
    return <MarkdownViewer source={code} />;
  }
  const directory = path.slice(0, path.lastIndexOf("/"));
  return <MarkdownViewer links={{ directory, loadImage, onOpenPath: repo.onOpenPath, root: projectRoot(repo.includedPaths, path) }} source={code} />;
}

function BlameToggle({ onChange, value }: { onChange(value: boolean): void; value: boolean }) {
  return (
    <button
      aria-pressed={value}
      className={cn(
        "h-8 rounded-md border px-2.5 text-xs font-medium transition",
        value
          ? "border-slate-300 dark:border-zinc-600 bg-white dark:bg-zinc-900 text-zinc-950 dark:text-zinc-50 shadow-sm"
          : "border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 text-slate-600 dark:text-zinc-400 hover:text-zinc-950 dark:hover:text-zinc-50"
      )}
      onClick={() => onChange(!value)}
      type="button"
    >
      Blame
    </button>
  );
}
