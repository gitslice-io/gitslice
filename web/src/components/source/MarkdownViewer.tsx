import { useEffect, useRef, useState } from "react";

import { isDarkTheme, renderMermaid, svgDataUrl } from "../../lib/diagram";
import { sanitizeHtml } from "../../lib/sanitize";

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
        const { marked } = await import("marked");
        const parsed = marked(source, { async: false });
        const html = await sanitizeHtml(parsed);

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

  const container = useRef<HTMLDivElement>(null);

  // Fenced ```mermaid blocks become diagrams. The diagram is shown as an image
  // (see lib/diagram), so the block's text is never trusted as markup.
  useEffect(() => {
    const root = container.current;
    if (!root || rendered.html === null) {
      return;
    }
    const blocks = Array.from(root.querySelectorAll<HTMLElement>("pre > code.language-mermaid"));
    let active = true;
    for (const block of blocks) {
      const pre = block.parentElement;
      renderMermaid(block.textContent ?? "", isDarkTheme()).then(
        (svg) => {
          if (!active || !pre?.isConnected) {
            return;
          }
          const image = document.createElement("img");
          image.alt = "Mermaid diagram";
          image.className = "h-auto max-w-full bg-white p-2";
          // Sized to the diagram, not to the page (see DiagramViewer).
          image.addEventListener("load", () => {
            if (image.naturalWidth > 0) {
              image.style.width = `${image.naturalWidth}px`;
            }
          });
          image.src = svgDataUrl(svg);
          pre.replaceWith(image);
        },
        () => undefined // the block stays as source
      );
    }
    return () => {
      active = false;
    };
  }, [rendered.html]);

  if (rendered.html !== null) {
    return (
      <div className="p-4" ref={container}>
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
