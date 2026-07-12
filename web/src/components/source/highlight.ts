// Read-only syntax highlighting via Shiki's fine-grained core API.
//
// All Shiki code is loaded through dynamic import() so it stays out of the main
// bundle and only loads when the source viewer is actually used. Uses the
// JavaScript regex engine (no oniguruma WASM ~620 KB) and a single theme, and
// lazy-loads only the grammar for the file being viewed (each language is its own
// code-split chunk) instead of the full bundled `shiki` highlighter.

import type { HighlighterCore } from "shiki/core";

const LIGHT_THEME = "github-light";
const DARK_THEME = "github-dark";
// "text" renders as plain (no grammar) and is always available in core.
const PLAIN = "text";

let highlighterPromise: Promise<HighlighterCore> | null = null;
const loadedLangs = new Set<string>();

function getHighlighter(): Promise<HighlighterCore> {
  if (!highlighterPromise) {
    highlighterPromise = (async () => {
      const [
        { createHighlighterCore },
        { createJavaScriptRegexEngine },
        lightTheme,
        darkTheme,
      ] = await Promise.all([
        import("shiki/core"),
        import("shiki/engine/javascript"),
        // Import the single theme we use directly instead of the `shiki/themes`
        // barrel, which code-splits a chunk referencing every bundled theme.
        import("@shikijs/themes/github-light"),
        import("@shikijs/themes/github-dark"),
      ]);
      return createHighlighterCore({
        themes: [lightTheme.default, darkTheme.default],
        langs: [],
        // forgiving: tolerate grammar patterns the JS engine can't express
        // rather than throwing — trades a little fidelity for no WASM.
        engine: createJavaScriptRegexEngine({ forgiving: true }),
      });
    })();
  }
  return highlighterPromise;
}

export async function highlightToHtml(
  code: string,
  language: string,
): Promise<string> {
  const highlighter = await getHighlighter();
  const { bundledLanguages } = await import("shiki/langs");

  let lang = language;
  if (lang !== PLAIN && !(lang in bundledLanguages)) {
    lang = PLAIN;
  }
  if (lang !== PLAIN && !loadedLangs.has(lang)) {
    await highlighter.loadLanguage(
      bundledLanguages[lang as keyof typeof bundledLanguages],
    );
    loadedLangs.add(lang);
  }

  return highlighter.codeToHtml(code, {
    lang,
    themes: { light: LIGHT_THEME, dark: DARK_THEME },
  });
}
