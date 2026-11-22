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
        `<li><a class="asset" href="/releases/download/${tag}/${a.name}">${a.name}</a><code class="sha">${a.sha}</code></li>`
    )
    .join("");
  const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>gs ${tag} · Gitslice Releases</title>
<style>
  :root { color-scheme: light dark; --bg: #f8fafc; --surface: #ffffff; --border: #e2e8f0; --fg: #09090b; --muted: #64748b; --accent: #0369a1; --code-bg: #f1f5f9; }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #09090b; --surface: #18181b; --border: #27272a; --fg: #fafafa; --muted: #a1a1aa; --accent: #7dd3fc; --code-bg: #27272a; }
  }
  * { box-sizing: border-box; }
  body { margin: 0; font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; background: var(--bg); color: var(--fg); line-height: 1.5; }
  header { border-bottom: 1px solid var(--border); background: var(--surface); }
  .bar { max-width: 48rem; margin: 0 auto; padding: 0.875rem 1rem; display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
  .brand { color: var(--fg); text-decoration: none; font-weight: 600; font-size: 0.9375rem; }
  .nav-link { color: var(--muted); text-decoration: none; font-size: 0.8125rem; font-weight: 500; }
  .nav-link:hover { color: var(--fg); }
  main { max-width: 48rem; margin: 0 auto; padding: 1.5rem 1rem 3rem; }
  h1 { margin: 0 0 0.25rem; font-size: 1.5rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; letter-spacing: -0.02em; }
  .sub { margin: 0 0 1.25rem; color: var(--muted); font-size: 0.875rem; }
  .install { background: var(--surface); border: 1px solid var(--border); border-radius: 0.5rem; padding: 0.875rem 1rem; margin-bottom: 1.25rem; font-size: 0.875rem; }
  .install p { margin: 0 0 0.5rem; color: var(--muted); font-size: 0.8125rem; }
  .install pre { margin: 0; overflow-x: auto; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8125rem; background: var(--code-bg); padding: 0.625rem 0.75rem; border-radius: 0.375rem; }
  .install .alt { margin: 0.625rem 0 0; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.85em; }
  .alt code { background: var(--code-bg); padding: 0.125rem 0.375rem; border-radius: 0.25rem; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: 0.5rem; overflow: hidden; }
  .card h2 { margin: 0; padding: 0.75rem 1rem; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); border-bottom: 1px solid var(--border); }
  ul { list-style: none; margin: 0; padding: 0; }
  li { padding: 0.75rem 1rem; border-bottom: 1px solid var(--border); display: flex; flex-direction: column; gap: 0.25rem; }
  li:last-child { border-bottom: 0; }
  a.asset { color: var(--accent); text-decoration: none; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.875rem; font-weight: 600; overflow-wrap: anywhere; }
  a.asset:hover { text-decoration: underline; }
  code.sha { color: var(--muted); font-size: 0.75rem; overflow-wrap: anywhere; }
</style>
</head>
<body>
  <header>
    <div class="bar">
      <a class="brand" href="/">Gitslice</a>
      <a class="nav-link" href="/slices/gitslice/gitslice?tags=true">All tags</a>
    </div>
  </header>
  <main>
    <h1>gs ${tag}</h1>
    <p class="sub">Release archives and SHA-256 checksums</p>
    <section class="install">
      <p>Install or upgrade</p>
      <pre>curl -fsSL https://gitslice.io/install.sh | sh</pre>
      <p class="alt">Or run <code>gs upgrade</code> if you already have <code>gs</code> installed.</p>
    </section>
    <section class="card">
      <h2>Files</h2>
      <ul>${rows}<li><a class="asset" href="/releases/download/${tag}/checksums.txt">checksums.txt</a></li></ul>
    </section>
  </main>
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
