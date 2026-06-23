import createDOMPurify, { type DOMPurify } from "dompurify";
import { marked } from "marked";

// A single purifier is reused across calls. It is created lazily so the module
// can be imported in environments where `window` is not yet available (e.g.
// before hydration); rendering only ever happens in the browser.
let purifier: DOMPurify | null = null;

function getPurifier(): DOMPurify {
  if (!purifier) {
    purifier = createDOMPurify(window);
    purifier.addHook("afterSanitizeAttributes", (node) => {
      if (node.tagName === "A" && node.hasAttribute("href")) {
        node.setAttribute("target", "_blank");
        node.setAttribute("rel", "noopener noreferrer");
      }
    });
  }
  return purifier;
}

// renderMarkdownToHtml parses GitHub-flavored markdown and returns sanitized
// HTML safe to drop into the DOM via dangerouslySetInnerHTML. Parsing is
// synchronous so callers can render in place — important for streaming text,
// which would otherwise flicker on every token while an async render settles.
export function renderMarkdownToHtml(source: string): string {
  // Sanitization needs a DOM. Conversation content is streamed in on the
  // client, so this never runs during SSR; guard anyway so an unexpected
  // server-side call degrades to escaped plain text instead of crashing.
  if (typeof window === "undefined") {
    return escapeHtml(source);
  }
  const parsed = marked(source, { async: false, gfm: true, breaks: true });
  return getPurifier().sanitize(parsed, { USE_PROFILES: { html: true } });
}

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
