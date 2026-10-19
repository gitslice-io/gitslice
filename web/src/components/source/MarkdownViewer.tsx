import { useEffect, useRef, useState, type MouseEvent } from "react";

import { isDarkTheme, renderMermaid, svgDataUrl } from "../../lib/diagram";
import { resolveRepoLink } from "../../lib/repoLinks";
import { sanitizeHtml } from "../../lib/sanitize";

interface MarkdownRenderState {
  error: boolean;
  html: string | null;
}

interface MarkdownViewerProps {
  source: string;
  // When the document lives in a repository directory, its relative links open
  // that path in the app (instead of resolving against the page's own address),
  // and its relative images, which the app cannot serve yet, show their alt text.
  links?: {
    directory: string;
    onOpenPath(path: string): void;
  };
}

export function MarkdownViewer({ source, links }: MarkdownViewerProps): JSX.Element {
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
  const linkDirectory = links?.directory;
  const openPath = links?.onOpenPath;

  function followRelativeLink(event: MouseEvent<HTMLElement>) {
    if (
      !openPath ||
      linkDirectory === undefined ||
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    ) {
      return;
    }
    const anchor = (event.target as HTMLElement | null)?.closest("a");
    const path = anchor ? resolveRepoLink(linkDirectory, anchor.getAttribute("href")) : null;
    if (!path) {
      return;
    }
    event.preventDefault();
    openPath(path);
  }

  // A relative image points at a repository file, which the page cannot serve
  // as an <img> source, so the alt text stands in for it.
  useEffect(() => {
    const root = container.current;
    if (!root || rendered.html === null || linkDirectory === undefined) {
      return;
    }
    for (const image of Array.from(root.querySelectorAll("img"))) {
      if (resolveRepoLink(linkDirectory, image.getAttribute("src")) === null) {
        continue;
      }
      const stand = document.createElement("span");
      stand.className = "text-slate-500 dark:text-zinc-400";
      stand.textContent = image.getAttribute("alt") ? `[${image.getAttribute("alt")}]` : "";
      image.replaceWith(stand);
    }
  }, [rendered.html, linkDirectory]);

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
      <div className="p-4" onClick={followRelativeLink} ref={container}>
        <div
          className="prose prose-slate dark:prose-invert prose-sm max-w-none [&_:not(pre)>code]:[overflow-wrap:anywhere] prose-a:text-sky-700 dark:prose-a:text-sky-300 prose-a:underline prose-code:text-zinc-900 dark:prose-code:text-zinc-100 prose-code:before:content-none prose-code:after:content-none prose-pre:border prose-pre:border-slate-200 dark:prose-pre:border-zinc-800 prose-pre:bg-slate-50 dark:prose-pre:bg-zinc-950 prose-pre:text-zinc-900 dark:prose-pre:text-zinc-100"
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
