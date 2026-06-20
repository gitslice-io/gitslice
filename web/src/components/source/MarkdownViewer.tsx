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
          className="prose prose-sm max-w-none prose-headings:font-serif prose-headings:text-on-surface prose-p:text-on-surface-variant prose-a:text-primary prose-a:underline prose-code:text-on-surface prose-code:before:content-none prose-code:after:content-none prose-pre:bg-surface-container prose-pre:text-on-surface"
          dangerouslySetInnerHTML={{ __html: rendered.html }}
        />
      </div>
    );
  }

  return (
    <div>
      {rendered.error ? (
        <div className="bg-tertiary-container px-4 py-3 text-xs text-tertiary">
          Markdown preview unavailable. Showing raw source.
        </div>
      ) : null}
      <pre className="min-w-max overflow-visible bg-surface-container-lowest p-4 text-sm leading-6 text-on-surface">
        <code>{source}</code>
      </pre>
    </div>
  );
}
