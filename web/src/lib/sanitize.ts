// Untrusted HTML (notebook outputs, Markdown) goes through DOMPurify before it
// reaches the page. DOMPurify loads on first use.
export async function sanitizeHtml(html: string, profile: "html" | "svg" = "html") {
  const { default: createDOMPurify } = await import("dompurify");
  const purifier = createDOMPurify(window);
  purifier.addHook("afterSanitizeAttributes", (node) => {
    if (node.tagName === "A" && node.hasAttribute("href")) {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer");
    }
  });
  return purifier.sanitize(html, { USE_PROFILES: profile === "svg" ? { svg: true, svgFilters: true } : { html: true } });
}

// ANSI colour and cursor escape sequences, as tracebacks and logs carry them.
// eslint-disable-next-line no-control-regex
const ANSI_PATTERN = /\u001b\[[0-9;?]*[ -/]*[@-~]/g;

export function stripAnsi(text: string) {
  return text.replace(ANSI_PATTERN, "");
}
