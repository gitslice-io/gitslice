import { Marked, type Tokens } from "marked";

// Blog posts are Markdown written in this slice, so they are rendered as they
// are (no sanitizer), on the server as well as in the browser: the article is
// in the page's HTML before any script runs.

export interface PostHeading {
  id: string;
  text: string;
}

export interface RenderedPost {
  html: string;
  headings: PostHeading[];
}

const LANGUAGE_LABELS: Record<string, string> = {
  bash: "Shell",
  console: "Shell",
  go: "Go",
  js: "JavaScript",
  json: "JSON",
  sh: "Shell",
  shell: "Shell",
  text: "Text",
  ts: "TypeScript",
  tsx: "TSX",
  yaml: "YAML",
  yml: "YAML",
};

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// headingId is a URL fragment for a heading: "2. What gets pushed" becomes
// "what-gets-pushed".
export function headingId(text: string): string {
  return (
    text
      .toLowerCase()
      .replace(/<[^>]+>/g, "")
      .replace(/&[a-z#0-9]+;/g, "")
      .replace(/^\s*\d+\.\s*/, "")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "") || "section"
  );
}

// codeInfo reads a fence's info string: "yaml title=.gitslice/automations.yaml".
export function codeInfo(info: string | undefined): {
  lang: string;
  title: string;
} {
  const [lang = "", ...rest] = (info ?? "").trim().split(/\s+/);
  const titleMatch = rest.join(" ").match(/title=(?:"([^"]*)"|(\S+))/);
  return {
    lang: lang.toLowerCase(),
    title: titleMatch?.[1] ?? titleMatch?.[2] ?? "",
  };
}

export function languageLabel(lang: string): string {
  return LANGUAGE_LABELS[lang] ?? (lang ? lang.toUpperCase() : "");
}

export function renderPost(markdown: string): RenderedPost {
  const headings: PostHeading[] = [];
  const used = new Map<string, number>();
  const marked = new Marked({ gfm: true });
  marked.use({
    renderer: {
      heading({ tokens, depth }: Tokens.Heading) {
        const inner = this.parser.parseInline(tokens);
        const base = headingId(inner);
        const seen = used.get(base) ?? 0;
        used.set(base, seen + 1);
        const id = seen ? `${base}-${seen + 1}` : base;
        if (depth === 2) {
          headings.push({ id, text: inner.replace(/<[^>]+>/g, "") });
        }
        return `<h${depth} id="${id}" class="blog-heading"><a class="blog-anchor" href="#${id}" aria-label="Link to this section">#</a>${inner}</h${depth}>\n`;
      },
      code({ text, lang }: Tokens.Code) {
        const info = codeInfo(lang);
        const code = `<code class="language-${escapeHtml(info.lang || "text")}">${escapeHtml(text)}</code>`;
        if (info.lang === "mermaid") {
          return `<pre class="blog-mermaid">${code}</pre>\n`;
        }
        const label = info.title || languageLabel(info.lang);
        const titled = info.title ? ` blog-code-titled` : "";
        return (
          `<figure class="blog-code not-prose${titled}">` +
          `<figcaption><span class="blog-code-title">${escapeHtml(label)}</span>` +
          (info.title && info.lang
            ? `<span class="blog-code-lang">${escapeHtml(languageLabel(info.lang))}</span>`
            : "") +
          `<button type="button" class="blog-code-copy" data-copy>Copy</button></figcaption>` +
          `<pre>${code}</pre></figure>\n`
        );
      },
      link({ href, title, tokens }: Tokens.Link) {
        const inner = this.parser.parseInline(tokens);
        const external =
          /^https?:\/\//.test(href) &&
          !/^https:\/\/gitslice\.io(\/|$)/.test(href);
        return (
          `<a href="${escapeHtml(href)}"${title ? ` title="${escapeHtml(title)}"` : ""}` +
          `${external ? ' target="_blank" rel="noopener noreferrer"' : ""}>${inner}</a>`
        );
      },
    },
  });
  const html = marked.parse(markdown, { async: false });
  return { html, headings };
}
