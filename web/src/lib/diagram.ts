// Rendering of diagram sources (Mermaid, Graphviz DOT) to SVG. Both engines are
// large and load on first use. The SVG is shown through an <img> with a data
// URL, which cannot run scripts or load anything, so the diagram source is
// never trusted with the page.

// Both engines are served as static files (public/vendor, copied by
// scripts/copy-vendor.mjs) and imported by URL on first use, so they cost the
// build nothing.
type Mermaid = typeof import("mermaid").default;
type VizModule = typeof import("@viz-js/viz");

const MERMAID_URL = "/vendor/mermaid/mermaid.esm.min.mjs";
const VIZ_URL = "/vendor/viz/viz.js";

let mermaidCount = 0;

export async function renderMermaid(source: string, dark: boolean): Promise<string> {
  const { default: mermaid } = (await import(/* @vite-ignore */ MERMAID_URL)) as { default: Mermaid };
  mermaid.initialize({
    securityLevel: "strict",
    startOnLoad: false,
    theme: dark ? "dark" : "default"
  });
  mermaidCount += 1;
  const { svg } = await mermaid.render(`diagram-${mermaidCount}`, source);
  return svg;
}

export async function renderDot(source: string): Promise<string> {
  const { instance } = (await import(/* @vite-ignore */ VIZ_URL)) as VizModule;
  const viz = await instance();
  return viz.renderString(source, { format: "svg" });
}

export function svgDataUrl(svg: string) {
  const bytes = new TextEncoder().encode(svg);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return `data:image/svg+xml;base64,${window.btoa(binary)}`;
}

export function isDarkTheme() {
  return typeof document !== "undefined" && document.documentElement.classList.contains("dark");
}
