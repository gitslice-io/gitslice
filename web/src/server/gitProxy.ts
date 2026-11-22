export function gitProxyTarget(request: Request): string | null {
  const url = new URL(request.url);
  if (url.pathname !== "/git" && !url.pathname.startsWith("/git/")) {
    return null;
  }

  const base = (import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/+$/, "");
  if (!base) {
    return null;
  }

  return `${base}${url.pathname}${url.search}`;
}
