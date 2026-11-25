// Release downloads are addressed through gitslice.io, so install.sh, gs
// upgrade and the docs never name the store that holds the bytes.
//
// Every release is built off GitHub (ops/release, started by the
// gitslice/release trigger) and stored in R2 under releases/<tag>/, with
// releases/latest.json naming the newest; the worker serves them from its
// RELEASES binding. Releases made before that were copied into R2 on
// 2026-11-17, so nothing is fetched from GitHub.

// Tags and asset names: letters, digits, '.', '_', '+' and '-', starting with
// a letter or digit. This keeps every key inside releases/ (no '/' or '..').
import { THEME_BOOTSTRAP_SCRIPT, THEME_STORAGE_KEY } from "../theme";

const segment = /^[A-Za-z0-9][A-Za-z0-9._+-]*$/;

// releasePath reports whether a request is for the releases tree, and its
// path segments after /releases when they name something it can serve:
// [], ["latest"], ["tag", tag], ["latest", "download", asset] or
// ["download", tag, asset]. Anything else under /releases is not a release.
export function releasePath(request: Request): string[] | null {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return null;
  }
  const { pathname } = new URL(request.url);
  if (pathname !== "/releases" && !pathname.startsWith("/releases/")) {
    return null;
  }
  const parts = pathname.split("/").filter(Boolean).slice(1);
  if (parts.length === 0) {
    return parts;
  }
  if (parts.length === 1 && parts[0] === "latest") {
    return parts;
  }
  if (parts.length === 3 && parts[0] === "latest" && parts[1] === "download" && segment.test(parts[2])) {
    return parts;
  }
  if (parts.length === 2 && parts[0] === "tag" && segment.test(parts[1])) {
    return parts;
  }
  if (parts.length === 3 && parts[0] === "download" && segment.test(parts[1]) && segment.test(parts[2])) {
    return parts;
  }
  return null;
}

// The part of an R2 bucket binding that serving releases uses.
export interface ReleaseObject {
  body: ReadableStream | null;
  size: number;
  text(): Promise<string>;
}

export interface ReleaseBucket {
  get(key: string): Promise<ReleaseObject | null>;
}

const textTypes: Record<string, string> = {
  ".tar.gz": "application/gzip",
  ".zip": "application/zip",
  ".txt": "text/plain; charset=utf-8",
  // Shown as text, so the install script can be read before it is run.
  ".sh": "text/plain; charset=utf-8",
  ".json": "application/json",
  // The agents demo release (agents-demo-2026-10-02) holds videos.
  ".mp4": "video/mp4",
  ".srt": "text/plain; charset=utf-8"
};

function contentType(asset: string) {
  const match = Object.keys(textTypes).find((suffix) => asset.endsWith(suffix));
  return match ? textTypes[match] : "application/octet-stream";
}

async function latestTag(bucket: ReleaseBucket): Promise<string | null> {
  const latest = await bucket.get("releases/latest.json");
  if (!latest) {
    return null;
  }
  try {
    const tag = (JSON.parse(await latest.text()) as { tag?: unknown }).tag;
    return typeof tag === "string" && segment.test(tag) ? tag : null;
  } catch {
    return null;
  }
}

function redirectTo(location: string, maxAge = 60) {
  return new Response(null, {
    status: 302,
    headers: { Location: location, "Cache-Control": `public, max-age=${maxAge}` }
  });
}

async function assetResponse(request: Request, bucket: ReleaseBucket, tag: string, asset: string) {
  const object = await bucket.get(`releases/${tag}/${asset}`);
  if (!object) {
    return null;
  }
  const headers = {
    "Cache-Control": "public, max-age=86400",
    "Content-Length": String(object.size),
    "Content-Type": contentType(asset),
    ...(asset.endsWith(".txt") || asset.endsWith(".json")
      ? {}
      : { "Content-Disposition": `attachment; filename="${asset}"` })
  };
  return new Response(request.method === "HEAD" ? null : object.body, { headers });
}

async function tagPage(request: Request, bucket: ReleaseBucket, tag: string) {
  const sums = await bucket.get(`releases/${tag}/checksums.txt`);
  if (!sums) {
    return null;
  }
  const assets = (await sums.text())
    .split("\n")
    .map((line) => line.trim().split(/\s+/))
    .filter((fields) => fields.length === 2 && segment.test(fields[1].replace(/^\*/, "")))
    .map(([sha, name]) => ({ name: name.replace(/^\*/, ""), sha }));
  const rows = assets
    .map(
      (a) =>
        `<tr><th scope="row"><a class="asset" href="/releases/download/${tag}/${a.name}">${a.name}</a></th><td><code class="sha">${a.sha}</code></td></tr>`
    )
    .join("");
  const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#f8fafc">
<link rel="icon" type="image/png" href="/favicon.png">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
<title>gs ${tag} · Gitslice Releases</title>
<script>${THEME_BOOTSTRAP_SCRIPT}</script>
<style>
  :root {
    color-scheme: light;
    --bg: #f8fafc;
    --surface: #ffffff;
    --header-bg: rgba(255, 255, 255, 0.95);
    --footer-bg: rgba(255, 255, 255, 0.8);
    --border: #e2e8f0;
    --border-subtle: #f1f5f9;
    --fg: #09090b;
    --fg-secondary: #334155;
    --muted: #64748b;
    --accent: #0369a1;
    --accent-hover: #075985;
    --code-bg: #f1f5f9;
    --pre-bg: #09090b;
    --pre-fg: #f1f5f9;
    --btn-hover: #f1f5f9;
    --badge-bg: #f8fafc;
    --dot: #10b981;
  }
  :root.dark {
    color-scheme: dark;
    --bg: #09090b;
    --surface: #18181b;
    --header-bg: rgba(9, 9, 11, 0.9);
    --footer-bg: rgba(9, 9, 11, 0.8);
    --border: #27272a;
    --border-subtle: #27272a;
    --fg: #fafafa;
    --fg-secondary: #d4d4d8;
    --muted: #a1a1aa;
    --accent: #7dd3fc;
    --accent-hover: #bae6fd;
    --code-bg: #27272a;
    --pre-bg: #09090b;
    --pre-fg: #f4f4f5;
    --btn-hover: #27272a;
    --badge-bg: #18181b;
    --dot: #10b981;
  }
  * { box-sizing: border-box; }
  html { scrollbar-gutter: stable; text-rendering: optimizeLegibility; -webkit-font-smoothing: antialiased; }
  body {
    margin: 0;
    min-height: 100dvh;
    display: flex;
    flex-direction: column;
    font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: var(--bg);
    color: var(--fg);
    line-height: 1.5;
    transition: background-color 200ms, color 200ms;
  }
  a { color: inherit; }
  a:focus-visible, button:focus-visible { outline: 2px solid #0ea5e9; outline-offset: 2px; border-radius: 0.375rem; }
  .skip { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  .skip:focus { position: fixed; top: 1rem; left: 1rem; width: auto; height: auto; clip-path: none; z-index: 50; padding: 0.5rem 0.875rem; background: var(--fg); color: var(--bg); font-size: 0.875rem; font-weight: 600; border-radius: 0.375rem; text-decoration: none; }
  header.topbar {
    position: sticky; top: 0; z-index: 30;
    border-bottom: 1px solid var(--border);
    background: var(--header-bg);
    backdrop-filter: blur(12px);
    padding: 0 1rem;
  }
  .bar {
    max-width: 100rem;
    min-height: 4rem;
    margin: 0 auto;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
  }
  .brand {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--fg);
    text-decoration: none;
    font-weight: 600;
    font-size: 0.875rem;
  }
  .brand img { width: 1.5rem; height: 1.5rem; display: block; }
  :root.dark .brand img, :root.dark .foot-brand img { filter: invert(1); }
  .nav-group { display: flex; align-items: center; gap: 0.25rem; }
  .nav-link {
    display: inline-flex; align-items: center;
    padding: 0.5rem 0.75rem;
    border-radius: 0.375rem;
    color: var(--muted);
    text-decoration: none;
    font-size: 0.875rem;
    font-weight: 500;
    transition: background-color 150ms, color 150ms;
  }
  .nav-link:hover { background: var(--btn-hover); color: var(--fg); }
  .theme-btn {
    margin-left: 0.25rem;
    display: inline-flex; align-items: center; justify-content: center;
    width: 2.25rem; height: 2.25rem;
    border-radius: 9999px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--muted);
    cursor: pointer;
    transition: background-color 150ms, color 150ms, border-color 150ms;
  }
  .theme-btn:hover { background: var(--btn-hover); color: var(--fg); }
  .theme-btn svg { width: 1.1rem; height: 1.1rem; }
  .icon-sun { display: none; }
  :root.dark .icon-moon { display: none; }
  :root.dark .icon-sun { display: block; }
  main { flex: 1; width: 100%; max-width: 56rem; margin: 0 auto; padding: 2rem 1rem 4rem; }
  .page-head { border-bottom: 1px solid var(--border); padding-bottom: 1.75rem; }
  .eyebrow { margin: 0; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.75rem; font-weight: 500; letter-spacing: 0.16em; text-transform: uppercase; color: var(--accent); }
  .title-row { margin-top: 0.625rem; display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 0.75rem; }
  h1 { margin: 0; font-size: clamp(1.75rem, 4vw, 2.5rem); font-weight: 600; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; letter-spacing: -0.03em; line-height: 1.1; }
  .sub { margin: 0.625rem 0 0; color: var(--muted); font-size: 0.9375rem; }
  .tag-link { color: var(--accent); text-decoration: none; font-size: 0.8125rem; font-weight: 500; }
  .tag-link:hover { text-decoration: underline; }
  .install { margin-top: 1.75rem; background: var(--surface); border: 1px solid var(--border); border-radius: 0.75rem; padding: 1.25rem; }
  .install h2 { margin: 0 0 0.75rem; font-size: 0.875rem; font-weight: 600; color: var(--fg); }
  .cmd-box { display: flex; align-items: center; gap: 0.5rem; background: var(--pre-bg); border: 1px solid var(--border); border-radius: 0.5rem; padding: 0.625rem 0.75rem; }
  .cmd-box pre { margin: 0; flex: 1; min-width: 0; overflow-x: auto; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8125rem; color: var(--pre-fg); }
  .copy-btn {
    flex-shrink: 0;
    padding: 0.25rem 0.625rem;
    border-radius: 0.375rem;
    border: 1px solid rgba(255, 255, 255, 0.18);
    background: rgba(255, 255, 255, 0.08);
    color: #f4f4f5;
    font-size: 0.75rem;
    font-weight: 500;
    cursor: pointer;
  }
  .copy-btn:hover { background: rgba(255, 255, 255, 0.16); }
  .alt { margin: 0.75rem 0 0; font-size: 0.8125rem; color: var(--muted); }
  .alt a { color: var(--accent); text-decoration: underline; text-underline-offset: 3px; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.85em; }
  .alt code { background: var(--code-bg); color: var(--fg); padding: 0.125rem 0.375rem; border-radius: 0.25rem; }
  .card { margin-top: 1.5rem; background: var(--surface); border: 1px solid var(--border); border-radius: 0.75rem; overflow: hidden; }
  .card-head { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; padding: 0.875rem 1.25rem; border-bottom: 1px solid var(--border); }
  .card-head h2 { margin: 0; font-size: 0.75rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em; color: var(--muted); }
  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; text-align: left; }
  caption { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  thead th { padding: 0.625rem 1.25rem; font-size: 0.75rem; font-weight: 500; text-transform: uppercase; letter-spacing: 0.05em; color: var(--muted); border-bottom: 1px solid var(--border); background: var(--badge-bg); }
  tbody tr { border-bottom: 1px solid var(--border-subtle); }
  tbody tr:last-child { border-bottom: 0; }
  tbody th, tbody td { padding: 0.75rem 1.25rem; vertical-align: top; }
  tbody th { font-weight: 600; white-space: nowrap; }
  a.asset { color: var(--accent); text-decoration: none; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8125rem; font-weight: 600; }
  a.asset:hover { color: var(--accent-hover); text-decoration: underline; }
  code.sha { color: var(--muted); font-size: 0.75rem; word-break: break-all; }
  footer.app-footer {
    border-top: 1px solid var(--border);
    background: var(--footer-bg);
    padding: 1.25rem 1rem;
    font-size: 0.75rem;
    color: var(--muted);
  }
  .foot-inner {
    max-width: 100rem;
    margin: 0 auto;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem 1.25rem;
  }
  .foot-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 0.625rem; }
  .foot-brand { display: inline-flex; align-items: center; gap: 0.375rem; font-weight: 600; color: var(--fg); text-decoration: none; }
  .foot-brand img { width: 1rem; height: 1rem; display: block; opacity: 0.85; }
  .foot-nav { display: flex; flex-wrap: wrap; align-items: center; gap: 0.5rem 0.875rem; }
  .foot-nav a { color: var(--muted); text-decoration: none; transition: color 150ms; }
  .foot-nav a:hover { color: var(--fg); }
  .status-link { display: inline-flex; align-items: center; gap: 0.375rem; }
  .status-dot { width: 0.375rem; height: 0.375rem; border-radius: 9999px; background: var(--dot); }
  @media (max-width: 640px) {
    .nav-hide-sm { display: none; }
    tbody th, tbody td { display: block; padding: 0.5rem 1rem; }
    tbody th { padding-bottom: 0.125rem; white-space: normal; }
    tbody td { padding-top: 0; padding-bottom: 0.75rem; }
    thead { display: none; }
  }
</style>
</head>
<body>
  <a class="skip" href="#release-main">Skip to main content</a>
  <header class="topbar">
    <div class="bar">
      <a class="brand" href="/">
        <img src="/brand/gitslice-mark-96.png" alt="" aria-hidden="true" width="24" height="24">
        <span>Gitslice</span>
      </a>
      <div class="nav-group">
        <nav aria-label="Primary" class="nav-group">
          <a class="nav-link nav-hide-sm" href="/">Home</a>
          <a class="nav-link" href="/doc">Docs</a>
          <a class="nav-link nav-hide-sm" href="/changelog">Changelog</a>
          <a class="nav-link" href="/slices/gitslice/gitslice?tags=true">All tags</a>
        </nav>
        <button class="theme-btn" id="theme-toggle" type="button" aria-label="Toggle color theme" title="Toggle light and dark theme">
          <svg class="icon-moon" aria-hidden="true" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.8" viewBox="0 0 24 24"><path d="M21 12.8A8.6 8.6 0 1 1 11.2 3 6.7 6.7 0 0 0 21 12.8Z"/></svg>
          <svg class="icon-sun" aria-hidden="true" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.8" viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.66 6.34l1.41-1.41"/></svg>
        </button>
      </div>
    </div>
  </header>
  <main id="release-main" tabindex="-1">
    <div class="page-head">
      <p class="eyebrow">CLI Release</p>
      <div class="title-row">
        <h1>gs ${tag}</h1>
        <a class="tag-link" href="/slices/gitslice/gitslice?tags=true">Browse all tags →</a>
      </div>
      <p class="sub">Prebuilt release archives and SHA-256 checksums served directly from Gitslice.</p>
    </div>

    <section class="install">
      <h2>Install or upgrade</h2>
      <div class="cmd-box">
        <pre id="install-cmd" role="region" aria-label="Install command" tabindex="0"><code>curl -fsSL https://gitslice.io/install.sh | sh</code></pre>
        <button class="copy-btn" id="copy-install" type="button">Copy</button>
      </div>
      <p class="alt">
        Already installed? Run <code>gs upgrade</code>, inspect <a href="/install.sh">install.sh</a>, or build from source with <code>go install gitslice.io/gitslice/cmd/gs@${tag}</code>.
      </p>
    </section>

    <section class="card">
      <div class="card-head">
        <h2>Release archives &amp; checksums</h2>
        <a class="tag-link" href="/releases/download/${tag}/checksums.txt">checksums.txt</a>
      </div>
      <div class="table-wrap">
        <table>
          <caption>Release archives and SHA-256 checksums for gs ${tag}</caption>
          <thead>
            <tr>
              <th scope="col">File</th>
              <th scope="col">SHA-256</th>
            </tr>
          </thead>
          <tbody>
            ${rows}
            <tr>
              <th scope="row"><a class="asset" href="/releases/download/${tag}/checksums.txt">checksums.txt</a></th>
              <td><code class="sha">SHA-256 manifest for ${tag}</code></td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </main>
  <footer class="app-footer">
    <div class="foot-inner">
      <div class="foot-meta">
        <a class="foot-brand" href="/" aria-label="Gitslice home">
          <img src="/brand/gitslice-mark-96.png" alt="" aria-hidden="true" width="16" height="16">
          <span>Gitslice</span>
        </a>
        <span aria-hidden="true">·</span>
        <span>© 2026 Gitslice</span>
        <span aria-hidden="true">·</span>
        <code>Apache 2.0</code>
      </div>
      <nav aria-label="Footer" class="foot-nav">
        <a href="/doc">Docs</a>
        <a href="/releases">CLI Releases</a>
        <a href="/changelog">Changelog</a>
        <a class="status-link" href="/status"><span class="status-dot" aria-hidden="true"></span>Status</a>
        <a href="/security">Security</a>
        <a href="/pricing">Pricing</a>
        <a href="/about">About</a>
        <a href="/privacy">Privacy</a>
        <a href="/terms">Terms</a>
        <a href="mailto:contact@gitslice.io">Contact</a>
      </nav>
    </div>
  </footer>
  <script>
    (() => {
      const btn = document.getElementById("theme-toggle");
      if (btn) {
        btn.addEventListener("click", () => {
          const root = document.documentElement;
          const next = root.classList.contains("dark") ? "light" : "dark";
          root.classList.toggle("dark", next === "dark");
          root.dataset.theme = next;
          root.style.colorScheme = next;
          document.querySelector('meta[name="theme-color"]')?.setAttribute("content", next === "dark" ? "#09090b" : "#f8fafc");
          try { localStorage.setItem("${THEME_STORAGE_KEY}", next); } catch {}
        });
      }
      const copy = document.getElementById("copy-install");
      if (copy && navigator.clipboard) {
        copy.addEventListener("click", () => {
          navigator.clipboard.writeText("curl -fsSL https://gitslice.io/install.sh | sh").then(() => {
            copy.textContent = "Copied";
            setTimeout(() => { copy.textContent = "Copy"; }, 1500);
          }).catch(() => {});
        });
      }
    })();
  </script>
</body>
</html>`;
  return new Response(request.method === "HEAD" ? null : html, {
    headers: { "Cache-Control": "public, max-age=300", "Content-Type": "text/html; charset=utf-8" }
  });
}

function plain(status: number, message: string, maxAge = 60) {
  return new Response(message + "\n", {
    status,
    headers: { "Cache-Control": `public, max-age=${maxAge}`, "Content-Type": "text/plain; charset=utf-8" }
  });
}

const notFound = () => plain(404, "No such gs release or file. See https://gitslice.io/releases/latest.");
const unavailable = () => plain(503, "gs releases are unavailable right now; try again in a minute.", 0);

// releaseResponse answers /releases URLs from R2:
//   /releases, /releases/latest          -> /releases/tag/<latest>
//   /releases/tag/<tag>                  -> a page listing the files
//   /releases/latest/download/<asset>    -> the latest release's file
//   /releases/download/<tag>/<asset>     -> that release's file
// A release or file that isn't there is 404; no bucket, or a failing one, is
// 503.
export async function releaseResponse(request: Request, bucket: ReleaseBucket | null | undefined): Promise<Response | null> {
  const parts = releasePath(request);
  if (!parts) {
    return null;
  }
  if (!bucket) {
    return unavailable();
  }
  try {
    if (parts.length === 0 || (parts.length === 1 && parts[0] === "latest")) {
      const tag = await latestTag(bucket);
      return tag ? redirectTo(`/releases/tag/${tag}`) : notFound();
    }
    if (parts.length === 2 && parts[0] === "tag") {
      return (await tagPage(request, bucket, parts[1])) ?? notFound();
    }
    if (parts.length === 3 && parts[0] === "latest" && parts[1] === "download") {
      const tag = await latestTag(bucket);
      // Through the tag's URL, so a cached "latest" never mixes two releases.
      return tag ? redirectTo(`/releases/download/${tag}/${parts[2]}`) : notFound();
    }
    return (await assetResponse(request, bucket, parts[1], parts[2])) ?? notFound();
  } catch {
    return unavailable();
  }
}
