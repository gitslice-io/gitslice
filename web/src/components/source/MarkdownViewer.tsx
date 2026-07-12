import { useEffect, useState } from "react";

interface MarkdownRenderState {
  error: boolean;
  html: string | null;
}

export function MarkdownViewer({ source }: { source: string }): JSX.Element {
  const [rendered, setRendered] = useState<MarkdownRenderState>({
    error: false,
    html: null
  });

  useEffect(() => {
    let active = true;

    async function renderMarkdown() {
      setRendered({ error: false, html: null });

      try {
        const [{ marked }, { default: createDOMPurify }] = await Promise.all([
          import("marked"),
          import("dompurify")
        ]);
        const purifier = createDOMPurify(window);

        purifier.addHook("afterSanitizeAttributes", (node) => {
          if (node.tagName === "A" && node.hasAttribute("href")) {
            node.setAttribute("target", "_blank");
            node.setAttribute("rel", "noopener noreferrer");
          }
        });

        const parsed = marked(source, { async: false });
        const html = purifier.sanitize(parsed, { USE_PROFILES: { html: true } });

        if (active) {
          setRendered({ error: false, html });
        }
      } catch {
        if (active) {
          setRendered({ error: true, html: null });
        }
      }
    }

    renderMarkdown();

    return () => {
      active = false;
    };
  }, [source]);

  if (rendered.html !== null) {
    return (
      <div className="p-4">
        <div
          className="prose prose-slate prose-sm max-w-none prose-a:text-sky-700 dark:prose-a:text-sky-300 prose-a:underline prose-code:text-zinc-900 dark:prose-code:text-zinc-100 prose-code:before:content-none prose-code:after:content-none prose-pre:border prose-pre:border-slate-200 dark:prose-pre:border-zinc-800 prose-pre:bg-slate-50 dark:prose-pre:bg-zinc-950 prose-pre:text-zinc-900 dark:prose-pre:text-zinc-100"
          dangerouslySetInnerHTML={{ __html: rendered.html }}
        />
      </div>
    );
  }

  return (
    <div>
      {rendered.error ? (
        <div className="border-b border-slate-200 dark:border-zinc-800 bg-amber-50 dark:bg-amber-950/30 px-4 py-3 text-xs text-amber-800 dark:text-amber-200">
          Markdown preview unavailable. Showing raw source.
        </div>
      ) : null}
      <pre className="min-w-max overflow-visible bg-white dark:bg-zinc-900 p-4 text-sm leading-6 text-zinc-900 dark:text-zinc-100">
        <code>{source}</code>
      </pre>
    </div>
  );
}
