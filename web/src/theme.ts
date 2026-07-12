export type ColorTheme = "dark" | "light";

export const THEME_STORAGE_KEY = "gitslice-theme";
export const THEME_CHANGE_EVENT = "gitslice:theme-change";

const DARK_MEDIA_QUERY = "(prefers-color-scheme: dark)";

export function storedTheme(): ColorTheme | null {
  if (typeof window === "undefined") return null;

  const value = window.localStorage.getItem(THEME_STORAGE_KEY);
  return value === "dark" || value === "light" ? value : null;
}

export function resolvedTheme(): ColorTheme {
  const stored = storedTheme();
  if (stored) return stored;

  return typeof window !== "undefined" &&
    window.matchMedia(DARK_MEDIA_QUERY).matches
    ? "dark"
    : "light";
}

export function applyTheme(theme: ColorTheme, persist = false) {
  if (typeof document === "undefined") return;

  const isDark = theme === "dark";
  const root = document.documentElement;
  root.classList.toggle("dark", isDark);
  root.dataset.theme = theme;
  root.style.colorScheme = theme;

  const themeColor = document.querySelector<HTMLMetaElement>(
    'meta[name="theme-color"]',
  );
  themeColor?.setAttribute("content", isDark ? "#09090b" : "#f8fafc");

  if (persist && typeof window !== "undefined") {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  }

  if (typeof window !== "undefined") {
    window.dispatchEvent(
      new CustomEvent<ColorTheme>(THEME_CHANGE_EVENT, { detail: theme }),
    );
  }
}

export function toggleTheme() {
  const nextTheme: ColorTheme = resolvedTheme() === "dark" ? "light" : "dark";
  applyTheme(nextTheme, true);
}

export function watchSystemTheme() {
  if (typeof window === "undefined") return () => undefined;

  const media = window.matchMedia(DARK_MEDIA_QUERY);
  const syncTheme = () => {
    if (!storedTheme()) {
      applyTheme(media.matches ? "dark" : "light");
    }
  };

  media.addEventListener("change", syncTheme);
  return () => media.removeEventListener("change", syncTheme);
}

// Runs in <head> before the app paints. Keep this dependency-free and in sync
// with the helpers above so a saved or system dark preference never flashes
// the light palette during hydration.
export const THEME_BOOTSTRAP_SCRIPT = `(() => {
  try {
    const stored = localStorage.getItem("${THEME_STORAGE_KEY}");
    const dark = stored === "dark" || (stored !== "light" && matchMedia("${DARK_MEDIA_QUERY}").matches);
    const root = document.documentElement;
    root.classList.toggle("dark", dark);
    root.dataset.theme = dark ? "dark" : "light";
    root.style.colorScheme = dark ? "dark" : "light";
    document.querySelector('meta[name="theme-color"]')?.setAttribute("content", dark ? "#09090b" : "#f8fafc");
  } catch {}
})();`;
